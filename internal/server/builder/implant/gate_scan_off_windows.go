//go:build windows && !evasionscan

package main

import "time"

// 默认实现（载荷未启用"主动反沙箱进程检测"时编译本文件）：
// 不做任何进程枚举、不含任何安全软件进程名字符串、不导入 toolhelp32 API。
// 详见 gate_scan_windows.go 的说明。
func hostDelay() time.Duration { return 0 }
