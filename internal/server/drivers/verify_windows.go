//go:build windows

// Windows 平台的驱动自检实现：
//
//  1. 签名/篡改校验：WinVerifyTrust(WINTRUST_ACTION_GENERIC_VERIFY_V2, WTD_CHOICE_FILE,
//     WTD_REVOKE_NONE, WTD_STATEACTION_VERIFY → CLOSE)，返回值 0 视为"签名有效且文件未被篡改"；
//  2. 签名者：CryptQueryObject → CryptMsgGetParam(CMSG_SIGNER_INFO) →
//     CertFindCertificateInStore(CERT_FIND_SUBJECT_CERT) → CertGetNameString(简单显示名)，失败留空；
//  3. 黑名单：只读注册表 HKLM\SYSTEM\CurrentControlSet\Control\CI\Config 的
//     VulnerableDriverBlocklistEnable，并探测本机微软黑名单数据文件是否可读。
//     **不下载、不内置任何名单数据，也不在代码里写死任何驱动名**（注释里的举例除外）。
//
// 指针宽度适配：所有 Win32 结构体都用 uintptr 表达指针字段，Go 会按目标架构自动对齐
// （386 → 4 字节对齐、amd64 → 8 字节对齐），与 MSVC 的默认 packing 一致，因此**同一份
// 结构体定义可同时用于 386 与 amd64**，无需按架构写两套。结构体大小在 init() 里断言。
package drivers

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// platformSupported 标记本平台是否提供真正的驱动自检。
const platformSupported = true

// WinVerifyTrust 的 action GUID：WINTRUST_ACTION_GENERIC_VERIFY_V2
// {00AAC56B-CD44-11d0-8CC2-00C04FC295EE}
const winTrustActionGenericVerifyV2 = "{00AAC56B-CD44-11d0-8CC2-00C04FC295EE}"

const (
	// WINTRUST_DATA.dwUIChoice：不弹任何 UI（服务端后台进程必备）。
	wtdUINone = 0
	// WINTRUST_DATA.fdwRevocationChecks：不做吊销检查（见下方签名超时说明）。
	wtdRevokeNone = 0
	// WINTRUST_DATA.dwUnionChoice：校验磁盘文件。
	wtdChoiceFile = 1
	// WINTRUST_DATA.dwStateAction：开始校验 / 释放状态句柄。
	wtdStateActionVerify = 1
	wtdStateActionClose  = 2
	// WINTRUST_DATA.dwProvFlags：证书链/CRL 只查本地缓存，不发起联网下载
	// （配合 WTD_REVOKE_NONE，正常情况下不会因为联网吊销检查而卡住）。
	wtdCacheOnlyURLRetrieval = 0x00001000

	// CryptQueryObject：按文件路径解析内嵌 PKCS#7 签名。
	certQueryObjectFile                  = 1
	certQueryContentFlagPKCS7SignedEmbed = 0x00000400
	certQueryFormatFlagBinary            = 0x00000002
	x509AsnEncoding                      = 0x00000001
	pkcs7AsnEncoding                     = 0x00010000
	cmsgSignerInfoParam                  = 8
	certFindSubjectCert                  = 0x000b0000
	certNameSimpleDisplayType            = 4
	// winTrustErrNone WinVerifyTrust 成功返回码。
	winTrustErrNone uint32 = 0
)

// 常见 WinVerifyTrust / 证书链错误码（LONG，按 uint32 保存）。
const (
	trustENosignature        = 0x800B0100
	trustEProviderUnknown    = 0x800B0001
	trustESubjectFormUnknown = 0x800B0003
	trustESubjectNotTrusted  = 0x800B0004
	trustEBadDigest          = 0x80096010
	trustEExplicitDistrust   = 0x800B0111
	certEExpired             = 0x800B0101
	certEUntrustedRoot       = 0x800B0109
	certERevoked             = 0x800B010C
	certEUntrustedTestRoot   = 0x800B010D
	certERevocationFailure   = 0x800B010E
	certEWrongUsage          = 0x800B0110
	cryptERevoked            = 0x80092010
	cryptERevocationOffline  = 0x80092013
	cryptESecuritySettings   = 0x80092026
	// errorInvalidParameter HRESULT_FROM_WIN32(ERROR_INVALID_PARAMETER)：见 winVerifyTrust 的
	// hwnd 兼容重试。
	errorInvalidParameter = 0x80070057
)

// winTrustFileInfo 对应 Win32 WINTRUST_FILE_INFO（x86=16 字节 / x64=32 字节）。
type winTrustFileInfo struct {
	cbStruct       uint32
	pcwszFilePath  uintptr
	hFile          uintptr
	pgKnownSubject uintptr
}

// winTrustData 对应 Win32 WINTRUST_DATA（x86=52 字节 / x64=88 字节）。
// 字段顺序与 wintrust.h 完全一致；指针字段用 uintptr，386/amd64 均由 Go 自动对齐。
type winTrustData struct {
	cbStruct            uint32
	pPolicyCallbackData uintptr
	pSIPClientData      uintptr
	dwUIChoice          uint32
	fdwRevocationChecks uint32
	dwUnionChoice       uint32
	pFile               uintptr
	dwStateAction       uint32
	hWVTStateData       uintptr
	pwszURLReference    uintptr
	dwProvFlags         uint32
	dwUIContext         uint32
	pSignatureSettings  uintptr
}

// cryptDataBlob 对应 Win32 CRYPT_DATA_BLOB。
// pbData 用 unsafe.Pointer 而不是 uintptr：布局完全相同（与指针同宽），但能让
// windows.CertInfo 里的 blob 字段直接赋值，也避免 go vet 的 unsafeptr 误报。
type cryptDataBlob struct {
	cbData uint32
	pbData unsafe.Pointer
}

// cryptAlgorithmIdentifier 对应 Win32 CRYPT_ALGORITHM_IDENTIFIER。
type cryptAlgorithmIdentifier struct {
	pszObjId   uintptr
	parameters cryptDataBlob
}

// cryptAttributes 对应 Win32 CRYPT_ATTRIBUTES。
type cryptAttributes struct {
	cAttr  uint32
	rgAttr uintptr
}

// cmsgSignerInfo 对应 Win32 CMSG_SIGNER_INFO（这里只用到 issuer/serialNumber）。
type cmsgSignerInfo struct {
	dwVersion               uint32
	issuer                  cryptDataBlob
	serialNumber            cryptDataBlob
	hashAlgorithm           cryptAlgorithmIdentifier
	hashEncryptionAlgorithm cryptAlgorithmIdentifier
	encryptedHash           cryptDataBlob
	authAttrs               cryptAttributes
	unauthAttrs             cryptAttributes
}

var (
	modWintrust        = windows.NewLazySystemDLL("wintrust.dll")
	procWinVerifyTrust = modWintrust.NewProc("WinVerifyTrust")

	modCrypt32           = windows.NewLazySystemDLL("crypt32.dll")
	procCryptMsgGetParam = modCrypt32.NewProc("CryptMsgGetParam")
	procCryptMsgClose    = modCrypt32.NewProc("CryptMsgClose")
)

// init 做结构体布局自检：WinVerifyTrust 会用 cbStruct 校验结构体大小，布局错了只会得到
// 一个莫名其妙的 ERROR_INVALID_PARAMETER，所以这里提前失败，便于定位问题。
func init() {
	if n := unsafe.Sizeof(winTrustData{}); n != 52 && n != 88 {
		panic(fmt.Sprintf("drivers: WINTRUST_DATA 布局异常（%d 字节，期望 32 位 52 / 64 位 88）", n))
	}
	if n := unsafe.Sizeof(winTrustFileInfo{}); n != 16 && n != 32 {
		panic(fmt.Sprintf("drivers: WINTRUST_FILE_INFO 布局异常（%d 字节，期望 32 位 16 / 64 位 32）", n))
	}
}

// platformSignature 做 Authenticode 签名校验，带软超时。
//
// 超时说明：WinVerifyTrust 在需要联网取证书链/吊销列表时可能阻塞数秒到数十秒。这里已经用
// WTD_REVOKE_NONE + WTD_CACHE_ONLY_URL_RETRIEVAL 关掉绝大部分联网行为，但仍加一层 4 秒
// 软超时兜底（SignatureTimeout）。超时后**无法取消**已经进入系统调用的那次调用（Windows
// 没有对应的取消接口），因此那个 goroutine 会继续跑到系统调用返回为止（channel 带缓冲，
// 不会泄漏），本次只返回一条提示，并**不缓存**该结论，下次请求会重试。
func platformSignature(path string, raw []byte) SignatureStatus {
	if err := procWinVerifyTrust.Find(); err != nil {
		return SignatureStatus{Warnings: []string{"wintrust.dll 不可用，跳过签名校验（" + err.Error() + "）"}}
	}
	abs := path
	if a, err := filepath.Abs(path); err == nil {
		abs = a
	}
	ch := make(chan SignatureStatus, 1)
	go func() { ch <- signatureBlocking(abs) }()
	select {
	case s := <-ch:
		return s
	case <-time.After(SignatureTimeout):
		return SignatureStatus{
			TimedOut: true,
			Warnings: []string{fmt.Sprintf("签名校验超时（>%s）：本次未能确认签名状态。首次校验可能因证书链/吊销列表联网检查而变慢，可在驱动自检接口重试", SignatureTimeout)},
		}
	}
}

// signatureBlocking 同步执行一次 WinVerifyTrust（调用方负责超时保护）。
func signatureBlocking(absPath string) SignatureStatus {
	code, err := winVerifyTrust(absPath)
	if err != nil {
		return SignatureStatus{Errors: []string{"签名校验调用失败：" + err.Error()}}
	}
	if code != winTrustErrNone {
		return SignatureStatus{Checked: true, Warnings: []string{winTrustErrMessage(code)}}
	}
	// 签名有效：尽量取签名者简单显示名；取不到不算失败，只给一条提示。
	signer, err := signerFromAuthenticode(absPath)
	if err != nil {
		return SignatureStatus{Checked: true, Signed: true, Warnings: []string{
			"签名有效（文件未被篡改），但未能取出签名者名称：" + err.Error() +
				"；常见原因是该 .sys 走目录签名（catalog，签名在 .cat 里）或签名者证书缺少简单显示名",
		}}
	}
	return SignatureStatus{Checked: true, Signed: true, Signer: signer}
}

// winVerifyTrust 调用 WinVerifyTrust 校验文件签名与完整性，返回 Win32 结果码。
// 结果码 0 = 签名有效且文件未被篡改。
func winVerifyTrust(absPath string) (uint32, error) {
	pathPtr, err := windows.UTF16PtrFromString(absPath)
	if err != nil {
		return 0, err
	}
	action, err := windows.GUIDFromString(winTrustActionGenericVerifyV2)
	if err != nil {
		return 0, fmt.Errorf("解析 WINTRUST_ACTION_GENERIC_VERIFY_V2 失败：%w", err)
	}

	fileInfo := winTrustFileInfo{
		cbStruct:      uint32(unsafe.Sizeof(winTrustFileInfo{})),
		pcwszFilePath: uintptr(unsafe.Pointer(pathPtr)),
	}
	data := winTrustData{
		cbStruct:            uint32(unsafe.Sizeof(winTrustData{})),
		dwUIChoice:          wtdUINone,
		fdwRevocationChecks: wtdRevokeNone,
		dwUnionChoice:       wtdChoiceFile,
		pFile:               uintptr(unsafe.Pointer(&fileInfo)),
		dwStateAction:       wtdStateActionVerify,
		dwProvFlags:         wtdCacheOnlyURLRetrieval,
	}

	// hwnd 传 NULL：dwUIChoice=WTD_UI_NONE 时该参数本应被忽略（微软示例用 INVALID_HANDLE_VALUE，
	// 多数 Go 实现传 0）。为防个别系统/沙箱里 NULL 被判成非法参数，下面在该错误码上重试一次。
	code := winVerifyTrustCall(&action, &data, 0)
	if code == errorInvalidParameter {
		if retry := winVerifyTrustCall(&action, &data, ^uintptr(0)); retry != errorInvalidParameter {
			code = retry
		}
	}

	// 必须再调一次 CLOSE 释放 WinVerifyTrust 分配的状态数据（hWVTStateData），否则会泄漏。
	// CLOSE 阶段不读 hwnd，沿用 0 即可。
	data.dwStateAction = wtdStateActionClose
	winVerifyTrustCall(&action, &data, 0)
	return code, nil
}

// winVerifyTrustCall 执行一次 WinVerifyTrust 调用（VERIFY 或 CLOSE 由 data.dwStateAction 决定）。
func winVerifyTrustCall(action *windows.GUID, data *winTrustData, hwnd uintptr) uint32 {
	ret, _, _ := procWinVerifyTrust.Call(
		hwnd,
		uintptr(unsafe.Pointer(action)),
		uintptr(unsafe.Pointer(data)),
	)
	return uint32(ret)
}

// winTrustErrMessage 把 WinVerifyTrust 结果码翻成中文原因。
func winTrustErrMessage(code uint32) string {
	switch code {
	case trustENosignature:
		return "驱动没有内嵌 Authenticode 签名（TRUST_E_NOSIGNATURE）：可能是完全未签名，也可能是目录签名（catalog，签名在 .cat 里）；两者在开启签名强制的系统上都可能被拒绝加载，请自行确认来源"
	case trustEBadDigest:
		return "签名与文件内容不匹配（TRUST_E_BAD_DIGEST）：驱动很可能已被篡改，请勿加载"
	case trustEExplicitDistrust:
		return "该签名证书被本机策略显式不信任（TRUST_E_EXPLICIT_DISTRUST）：很可能命中了易受攻击驱动黑名单或管理员策略"
	case cryptESecuritySettings:
		return "被本机安全设置/代码完整性策略拒绝（CRYPT_E_SECURITY_SETTINGS）：很可能命中易受攻击驱动黑名单"
	case trustESubjectNotTrusted:
		return "签名主体不受信任（TRUST_E_SUBJECT_NOT_TRUSTED）"
	case trustESubjectFormUnknown:
		return "签名主体格式无法识别（TRUST_E_SUBJECT_FORM_UNKNOWN）"
	case trustEProviderUnknown:
		return "找不到可用的签名校验提供者（TRUST_E_PROVIDER_UNKNOWN）"
	case certEExpired:
		return "签名证书已过期（CERT_E_EXPIRED）"
	case certERevoked:
		return "签名证书已被吊销（CERT_E_REVOKED）"
	case cryptERevoked:
		return "签名证书已被吊销（CRYPT_E_REVOKED）"
	case certERevocationFailure:
		return "无法确认证书吊销状态（CERT_E_REVOCATION_FAILURE）：可能无法联网访问吊销服务器"
	case cryptERevocationOffline:
		return "吊销服务器离线，无法确认证书状态（CRYPT_E_REVOCATION_OFFLINE）"
	case certEUntrustedRoot, certEUntrustedTestRoot:
		return "证书链根证书不受本机信任（CERT_E_UNTRUSTEDROOT）：签名无效"
	case certEWrongUsage:
		return "签名证书用途不匹配（CERT_E_WRONG_USAGE）：不是代码签名证书"
	default:
		return fmt.Sprintf("签名校验未通过（WinVerifyTrust=0x%08X）", code)
	}
}

// signerFromAuthenticode 取 Authenticode 签名者（证书简单显示名）。
//
// 实现成本说明：完整链路是 CryptQueryObject → CryptMsgGetParam(CMSG_SIGNER_INFO) →
// CertFindCertificateInStore(CERT_FIND_SUBJECT_CERT) → CertGetNameString。其中
// CryptMsgGetParam/CryptMsgClose 不在 golang.org/x/sys/windows 里，这里用 LazyDLL 手写调用；
// 结构体只用 uintptr + 嵌套结构体描述，天然适配 386/amd64。
//
// 取舍：目录签名（catalog）的 .sys 没有内嵌 PKCS#7，这里会失败并返回空签名者 +
// 一条说明性警告——要支持它得走 CryptCATAdminCalcHashFromFileHandle + CryptCATAdminEnumCatalogFromHash
// 再解析 .cat，代码量翻倍且当前 BYOVD 场景基本都用内嵌签名，故不做（不伪造结论）。
func signerFromAuthenticode(absPath string) (string, error) {
	pathPtr, err := windows.UTF16PtrFromString(absPath)
	if err != nil {
		return "", err
	}
	var (
		encodingType uint32
		contentType  uint32
		formatType   uint32
		store        windows.Handle
		msg          windows.Handle
		ctx          unsafe.Pointer
	)
	if err := windows.CryptQueryObject(
		certQueryObjectFile,
		unsafe.Pointer(pathPtr),
		certQueryContentFlagPKCS7SignedEmbed,
		certQueryFormatFlagBinary,
		0,
		&encodingType, &contentType, &formatType,
		&store, &msg, &ctx,
	); err != nil {
		return "", fmt.Errorf("CryptQueryObject: %w", err)
	}
	if store != 0 {
		defer windows.CertCloseStore(store, 0)
	}
	if msg != 0 {
		defer cryptMsgClose(msg)
	}
	if msg == 0 {
		return "", errors.New("文件里没有内嵌 PKCS#7 签名（可能是目录签名/catalog 或未签名）")
	}

	// 一次取 CMSG_SIGNER_INFO 大小，一次取内容。
	var size uint32
	if err := cryptMsgGetParam(msg, cmsgSignerInfoParam, nil, &size); err != nil {
		return "", fmt.Errorf("CryptMsgGetParam(取大小): %w", err)
	}
	if size == 0 {
		return "", errors.New("CMSG_SIGNER_INFO 为空")
	}
	buf := make([]byte, size)
	if err := cryptMsgGetParam(msg, cmsgSignerInfoParam, buf, &size); err != nil {
		return "", fmt.Errorf("CryptMsgGetParam(取内容): %w", err)
	}
	si := (*cmsgSignerInfo)(unsafe.Pointer(&buf[0]))

	// 用签名者的 issuer + serialNumber 在 store 里定位证书。
	certInfo := windows.CertInfo{
		SerialNumber: windows.CryptIntegerBlob{
			Size: si.serialNumber.cbData,
			Data: (*byte)(si.serialNumber.pbData),
		},
		Issuer: windows.CertNameBlob{
			Size: si.issuer.cbData,
			Data: (*byte)(si.issuer.pbData),
		},
	}
	cert, err := windows.CertFindCertificateInStore(
		store,
		x509AsnEncoding|pkcs7AsnEncoding,
		0,
		certFindSubjectCert,
		unsafe.Pointer(&certInfo),
		nil,
	)
	if err != nil {
		return "", fmt.Errorf("CertFindCertificateInStore: %w", err)
	}
	defer windows.CertFreeCertificateContext(cert)

	n := windows.CertGetNameString(cert, certNameSimpleDisplayType, 0, nil, nil, 0)
	if n <= 1 {
		return "", errors.New("证书没有简单显示名")
	}
	name := make([]uint16, n)
	if got := windows.CertGetNameString(cert, certNameSimpleDisplayType, 0, nil, &name[0], n); got <= 1 {
		return "", errors.New("CertGetNameString 未返回名称")
	}
	return windows.UTF16ToString(name), nil
}

// cryptMsgGetParam 包装 CryptMsgGetParam（x/sys 未提供该函数）。
// buf 为 nil 时只回填大小。
func cryptMsgGetParam(msg windows.Handle, paramType uint32, buf []byte, size *uint32) error {
	var ptr uintptr
	if len(buf) > 0 {
		ptr = uintptr(unsafe.Pointer(&buf[0]))
	}
	r1, _, e1 := procCryptMsgGetParam.Call(
		uintptr(msg),
		uintptr(paramType),
		0,
		ptr,
		uintptr(unsafe.Pointer(size)),
	)
	if r1 == 0 {
		if e1 != nil && e1 != syscall.Errno(0) {
			return e1
		}
		return errors.New("CryptMsgGetParam 返回失败")
	}
	return nil
}

// cryptMsgClose 包装 CryptMsgClose（x/sys 未提供该函数）。
func cryptMsgClose(msg windows.Handle) {
	procCryptMsgClose.Call(uintptr(msg))
}

// platformBlocklist 读取本机"易受攻击驱动黑名单"启用状态，并探测微软黑名单数据文件是否可读。
//
// 只读两项本机事实，不做任何下载、不内置名单：
//   - 注册表 HKLM\SYSTEM\CurrentControlSet\Control\CI\Config\VulnerableDriverBlocklistEnable
//     （不存在或为 0 = 未启用）；
//   - %windir%\System32\CodeIntegrity\driversipolicy.p7b 是否存在（32 位进程在 64 位系统上
//     访问 System32 会被 WOW64 重定向到 SysWOW64，所以同时看 Sysnative）。
func platformBlocklist() BlocklistStatus {
	st := BlocklistStatus{}

	// —— 注册表状态 ——
	// 注：WOW64 注册表重定向只作用于 HKLM\Software，SYSTEM\CurrentControlSet 不受 32 位进程影响。
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\CI\Config`, registry.QUERY_VALUE)
	if err != nil {
		st.Reason = "未启用：读不到 HKLM\\SYSTEM\\CurrentControlSet\\Control\\CI\\Config（" + err.Error() + "），视为黑名单未启用"
	} else {
		v, _, verr := k.GetIntegerValue("VulnerableDriverBlocklistEnable")
		_ = k.Close()
		switch {
		case verr != nil:
			st.Reason = "未启用：HKLM\\SYSTEM\\CurrentControlSet\\Control\\CI\\Config 下没有 VulnerableDriverBlocklistEnable 值"
		case v == 0:
			st.Reason = "未启用：VulnerableDriverBlocklistEnable = 0"
		default:
			st.Enabled = true
			st.Reason = fmt.Sprintf("已启用：VulnerableDriverBlocklistEnable = %d，内核会按微软易受攻击驱动黑名单拒绝加载此类驱动", v)
			st.Warnings = append(st.Warnings,
				"本机已启用微软易受攻击驱动黑名单（VulnerableDriverBlocklistEnable=1）：名单内的驱动会被内核拒绝，StartService 报 1275（ERROR_DRIVER_BLOCKED）；请换用未在黑名单内、且带有效签名的驱动")
		}
	}

	// —— 本机黑名单数据文件（微软自己下发的那份，只探测可读性，服务端不解析其内容）——
	policyPath, found := findDriverPolicyFile()
	if found {
		st.Warnings = append(st.Warnings, fmt.Sprintf(
			"本机存在微软易受攻击驱动黑名单数据 %s：若黑名单启用（VulnerableDriverBlocklistEnable=1 或开启了「内存完整性」HVCI），内核会静默拒绝该名单内的驱动，StartService 报 1275（ERROR_DRIVER_BLOCKED）",
			policyPath))
	} else {
		st.Warnings = append(st.Warnings, "本机未发现微软易受攻击驱动黑名单数据（已检查 %windir%\\System32\\CodeIntegrity\\driversipolicy.p7b 与 Sysnative 下的同名文件）：通常说明黑名单未启用，但仍可能被 HVCI/自定义 CI 策略或杀软驱动拦截")
	}
	return st
}

// findDriverPolicyFile 探测本机微软易受攻击驱动黑名单数据文件是否存在。
func findDriverPolicyFile() (string, bool) {
	root := strings.TrimSpace(os.Getenv("SystemRoot"))
	if root == "" {
		root = `C:\Windows`
	}
	cands := []string{
		filepath.Join(root, "System32", "CodeIntegrity", "driversipolicy.p7b"),
		filepath.Join(root, "Sysnative", "CodeIntegrity", "driversipolicy.p7b"), // 32 位进程在 64 位系统上的入口
	}
	for _, p := range cands {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
	}
	return strings.Join(cands, " 或 "), false
}
