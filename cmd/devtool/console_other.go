//go:build !windows

package main

// consoleNeedsGBK 在非 Windows 上恒为 false：Linux / macOS 终端是 UTF-8，
// 输出无需转码（转码反而会破坏中文）。
func consoleNeedsGBK() bool { return false }
