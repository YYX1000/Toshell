// Package drivers 管理 **操作员自备** 的 BYOVD 驱动（v1.3.3 起不再内置任何驱动）。
//
// 为什么不再内置：内置驱动意味着**服务端与载荷里都带着驱动名与 IOCTL 明文**，
// 既是稳定的静态特征（多数 AV/EDR 直接按易受攻击驱动名告警），也让我们背上第三方
// 二进制的分发与合规负担。现在改为「驱动由操作员提供、元数据由操作员声明」：
//
//	release/drivers/                随包目录，放自己的 *.sys（可选）
//	release/drivers/manifest.json   可选，描述每个驱动的元数据
//	data/drivers/                   运行时目录（也可放这里）
//
// manifest.json 形如：
//
//	{
//	  "drivers": [
//	    {
//	      "file": "yourdriver.sys",
//	      "name": "yourdriver",
//	      "purpose": "kill",              // kill = 提供进程终止 IOCTL；rw = 任意内核读写
//	      "service": "yourdriver",         // SCM 服务名
//	      "device": "\\\\.\\yourdriver",    // 设备路径
//	      "ioctl": "0x222048",             // 终止进程的 IOCTL（支持十六进制字符串或数字）
//	      "kill_pid_size": 4,              // IOCTL 入参 PID 字段字节数
//	      "description": "用途备注",
//	      "signed": "签名者（人工核对用）"
//	    }
//	  ]
//	}
//
// 没有 manifest 时 List() 仍会列出目录里的 .sys（元数据留空，UI 会提示补全）；
// sha256 一律实时计算，便于操作员加载前自行核对。
//
// ⚠️ 仅供授权红队/渗透测试使用；加载驱动前请自行确认签名与来源合法。
package drivers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Driver 描述一个可用驱动。
type Driver struct {
	Name        string `json:"name"`
	File        string `json:"file"`
	Description string `json:"description"`
	// Purpose 用途：kill = 无鉴权进程终止 IOCTL；rw = 任意内核读写（PPL 场景）。
	Purpose string `json:"purpose"`
	Device  string `json:"device"`
	Service string `json:"service"`
	// IOCTL 终止进程的 IOCTL 码（METHOD_BUFFERED，入参首个 DWORD = PID）。
	IOCTL uint32 `json:"ioctl"`
	// KillPIDSize 终止 IOCTL 的 PID 字段字节数（InputBufferLength 校验用，默认 4）。
	KillPIDSize uint32 `json:"kill_pid_size"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	Signed      string `json:"signed"`
	// Path 磁盘绝对路径（供下载/加载任务使用）。
	Path string `json:"-"`
}

// manifestFile manifest.json 结构。
type manifestFile struct {
	Drivers []struct {
		File        string      `json:"file"`
		Name        string      `json:"name"`
		Description string      `json:"description"`
		Purpose     string      `json:"purpose"`
		Device      string      `json:"device"`
		Service     string      `json:"service"`
		IOCTL       interface{} `json:"ioctl"`
		KillPIDSize uint32      `json:"kill_pid_size"`
		Signed      string      `json:"signed"`
	} `json:"drivers"`
}

// SearchDirs 返回扫描驱动的目录（按优先级）：
//  1. 服务端可执行文件同目录的 drivers/（发布包布局）
//  2. 当前工作目录的 drivers/
//  3. data/drivers/（运行时放置）
func SearchDirs() []string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), "drivers"))
	}
	dirs = append(dirs, filepath.Join(".", "drivers"), filepath.Join("data", "drivers"))

	seen := map[string]bool{}
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		abs, err := filepath.Abs(d)
		if err != nil || seen[abs] {
			continue
		}
		seen[abs] = true
		out = append(out, abs)
	}
	return out
}

// List 列出所有可用驱动（.sys 实时算 sha256，元数据来自同目录 manifest.json）。
func List() []Driver {
	var out []Driver
	seen := map[string]bool{}
	for _, dir := range SearchDirs() {
		meta := readManifest(dir)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".sys") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			if seen[strings.ToLower(path)] {
				continue
			}
			seen[strings.ToLower(path)] = true

			d := Driver{Name: strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())), File: e.Name(), Path: path}
			if info, err := e.Info(); err == nil {
				d.Size = info.Size()
			}
			if raw, err := os.ReadFile(path); err == nil {
				sum := sha256.Sum256(raw)
				d.SHA256 = hex.EncodeToString(sum[:])
			}
			for _, m := range meta.Drivers {
				if strings.EqualFold(m.File, e.Name()) || (m.Name != "" && strings.EqualFold(m.Name, d.Name)) {
					if m.Name != "" {
						d.Name = m.Name
					}
					d.Description = m.Description
					d.Purpose = m.Purpose
					d.Device = m.Device
					d.Service = m.Service
					d.IOCTL = parseIOCTL(m.IOCTL)
					d.KillPIDSize = m.KillPIDSize
					d.Signed = m.Signed
					break
				}
			}
			if d.KillPIDSize == 0 {
				d.KillPIDSize = 4
			}
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get 按名称或文件名取驱动与其字节内容。
func Get(name string) (Driver, []byte, error) {
	for _, d := range List() {
		if d.Name == name || d.File == name {
			raw, err := os.ReadFile(d.Path)
			if err != nil {
				return d, nil, err
			}
			return d, raw, nil
		}
	}
	return Driver{}, nil, fmt.Errorf("未找到驱动 %q：请把 .sys 放到 %s（可选配 manifest.json 声明设备名/服务名/IOCTL）",
		name, strings.Join(SearchDirs(), " 或 "))
}

// KillProfile 返回可用于「进程终止」的驱动档案（purpose=kill 且已填 device+ioctl）。
// 没有可用档案时返回 false —— 此时 byovd_kill 必须由调用方显式提供设备名与 IOCTL。
func KillProfile() (Driver, bool) {
	for _, d := range List() {
		if strings.EqualFold(d.Purpose, "kill") && d.Device != "" && d.IOCTL != 0 {
			return d, true
		}
	}
	return Driver{}, false
}

func readManifest(dir string) manifestFile {
	var m manifestFile
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return m
	}
	_ = json.Unmarshal(raw, &m)
	return m
}

// parseIOCTL 兼容 "0x222048"（字符串）与 2236488（数字）两种写法。
func parseIOCTL(v interface{}) uint32 {
	switch t := v.(type) {
	case float64:
		return uint32(t)
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0
		}
		if n, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(s), "0x"), 16, 32); err == nil {
			return uint32(n)
		}
		if n, err := strconv.ParseUint(s, 10, 32); err == nil {
			return uint32(n)
		}
	}
	return 0
}
