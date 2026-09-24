//go:build windows

package main

import "golang.org/x/sys/windows"

// consoleNeedsGBK 报告当前控制台输出代码页不是 UTF-8（典型是 936 = GBK）。
//
// 背景：Go 程序向 stdout 写的是 UTF-8 字节，而 Windows cmd 默认代码页是 936。
// 直接把 UTF-8 喂给 936 控制台，中文就变成乱码 —— Toshell.bat 里显式做了
// `chcp 936`，所以从它调用 devtool 时这个问题必然出现。
// 判断而非硬编码，是为了照顾已经 `chcp 65001` 或重定向到 UTF-8 文件的场景。
func consoleNeedsGBK() bool {
	cp, err := windows.GetConsoleOutputCP()
	if err != nil {
		return false
	}
	return cp != 65001 // 65001 = UTF-8
}
