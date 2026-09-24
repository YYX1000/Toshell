//go:build !windows

// 非 Windows 平台的占位实现：驱动自检（Authenticode 签名 / 易受攻击驱动黑名单）依赖
// Windows 的 WinVerifyTrust 与 CI 注册表，其他平台无从校验，因此这里只给一条说明性警告，
// 保证整个包能跨平台编译。纯逻辑（CompareHash / BuildVerifyResult / Summary）与平台无关，
// 在任意平台都可用，单测直接调用它们。
package drivers

// platformSupported 标记本平台是否提供真正的驱动自检。
const platformSupported = false

// platformSignature 非 Windows 平台不做签名校验。
func platformSignature(path string, raw []byte) SignatureStatus {
	return SignatureStatus{}
}

// platformBlocklist 非 Windows 平台没有微软易受攻击驱动黑名单。
func platformBlocklist() BlocklistStatus {
	return BlocklistStatus{}
}
