//go:build !windows

package main

// loadDLLMem 反射加载 DLL：非 Windows 平台不支持。
func loadDLLMem(dataB64, entryName string) (string, int32, string) {
	return "", -1, "DLL reflective loading is only supported on Windows"
}

// loadEXEMem 反射式内存执行 EXE（含参数）：非 Windows 平台不支持
// （Linux/macOS 侧用 memfd + execve 的路径，见 ROADMAP P0.3）。
func loadEXEMem(dataB64, args, imageName string, waitMs int) (string, int32, string) {
	return "", -1, "in-memory EXE execution is only supported on Windows (use exec with a temp file on Unix)"
}
