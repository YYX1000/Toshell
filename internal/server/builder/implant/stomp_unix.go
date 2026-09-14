//go:build !windows && !light

package main

// module stomp (shellcode 驻留到模块空洞) 依赖 Windows 的 PE/内存映像操作,
// 仅在 Windows 的 full 档实现(stomp_windows.go); light 档由 features_stub_light.go 提供空实现。
// 本文件补齐「非 Windows 且非 light」这一组合,否则 macOS/Linux 的 full 档构建会
// 因 undefined: stompShellcode 失败。
func stompShellcode(data string) (string, int32, string) {
	return "", -1, "module stomp is only supported on Windows"
}
