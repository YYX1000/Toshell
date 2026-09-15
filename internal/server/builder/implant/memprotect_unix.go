//go:build !windows

package main

import "errors"

// ─── 非 Windows 平台的同签名 stub ────────────────────────────────────────────
//
// RWX 相关的内存自申请/自改保护只出现在 Windows 植入端路径（反射加载 PE、
// 内存执行 BOF、模块空洞驻留、跨进程注入等）。Linux/macOS 侧目前没有任何
// 自申请可执行内存的代码，因此这里只提供与 memprotect_windows.go 完全一致的
// 签名，避免把 Windows 实现细节泄漏到跨平台文件里，也方便将来补齐实现。

var errMemProtectUnsupported = errors.New("memprotect: not supported on this platform")

// allocRW 在非 Windows 平台未实现。
func allocRW(size uintptr) (uintptr, uintptr, error) {
	return 0, 0, errMemProtectUnsupported
}

// protectRX 在非 Windows 平台未实现。
func protectRX(addr, size uintptr) error {
	return errMemProtectUnsupported
}

// protectRW 在非 Windows 平台未实现。
func protectRW(addr, size uintptr) error {
	return errMemProtectUnsupported
}

// protectRWGet 在非 Windows 平台未实现。
func protectRWGet(addr, size uintptr) (uint32, error) {
	return 0, errMemProtectUnsupported
}

// restoreProtect 在非 Windows 平台未实现。
func restoreProtect(addr, size uintptr, old uint32) error {
	return errMemProtectUnsupported
}

// withWritable 在非 Windows 平台未实现。
func withWritable(addr, size uintptr, fn func()) error {
	return errMemProtectUnsupported
}
