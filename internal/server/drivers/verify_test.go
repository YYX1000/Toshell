// 加载前自检的纯逻辑单测。
//
// 刻意**不调用真实签名校验**（WinVerifyTrust / CryptQueryObject）：那会依赖本机证书链、
// 可能触发联网吊销检查，随机文件必然验签失败，测试会变成环境依赖。这里只覆盖平台无关的
// 纯逻辑：CompareHash（sha256 比对）、BuildVerifyResult（结论合并）、Summary（中文结论），
// 以及"临时目录造 .sys + manifest.json"这条真实读盘路径（不含验签）。
package drivers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTempDriver 造一个临时的 .sys + manifest.json，返回 .sys 路径与实际 sha256。
func writeTempDriver(t *testing.T, name string, body []byte, manifest string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("写入临时驱动失败: %v", err)
	}
	if manifest != "" {
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o600); err != nil {
			t.Fatalf("写入 manifest.json 失败: %v", err)
		}
	}
	return path, sha256Hex(body)
}

// TestManifestHashMatch manifest 声明的 sha256 与真实文件一致 → 不报错。
func TestManifestHashMatch(t *testing.T) {
	body := []byte("fake-sys-bytes-1")
	dir := t.TempDir()
	path := filepath.Join(dir, "good.sys")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	sum := sha256Hex(body)
	manifest := `{"drivers":[{"file":"good.sys","name":"good","sha256":"` + strings.ToUpper(sum) + `"}]}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("写入 manifest 失败: %v", err)
	}

	expected, declared := manifestExpectedFor(path)
	if !strings.EqualFold(expected, sum) {
		t.Fatalf("manifest 期望哈希解析错误: got %q want %q", expected, sum)
	}
	if declared != "" {
		t.Fatalf("未声明 signed 时应为空: %q", declared)
	}

	res := BuildVerifyResult(sum, expected, "", SignatureStatus{Checked: true, Signed: true, Signer: "Contoso Ltd."}, BlocklistStatus{})
	if !res.HashOK {
		t.Fatalf("哈希一致却判定 HashOK=false: %+v", res)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("哈希一致不应有 Errors: %v", res.Errors)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("哈希一致且已签名不应有 Warnings: %v", res.Warnings)
	}
	if !strings.Contains(res.Summary(), "自检通过") {
		t.Fatalf("Summary 应表达通过: %q", res.Summary())
	}
}

// TestManifestHashMismatch manifest 声明的 sha256 与真实文件不一致 → Errors 非空且含中文原因。
func TestManifestHashMismatch(t *testing.T) {
	body := []byte("fake-sys-bytes-2")
	path, sum := writeTempDriver(t, "tampered.sys", body,
		`{"drivers":[{"file":"tampered.sys","name":"tampered","sha256":"`+strings.Repeat("ab", 32)+`"}]}`)

	expected, _ := manifestExpectedFor(path)
	if expected == "" || strings.EqualFold(expected, sum) {
		t.Fatalf("期望值应存在且与真实哈希不同: expected=%q actual=%q", expected, sum)
	}

	res := BuildVerifyResult(sum, expected, "", SignatureStatus{Checked: true, Signed: true}, BlocklistStatus{})
	if res.HashOK {
		t.Fatalf("哈希不一致却判定 HashOK=true: %+v", res)
	}
	if len(res.Errors) == 0 {
		t.Fatal("哈希不一致必须产生 Errors（调用方据此拒绝下发）")
	}
	joined := strings.Join(res.Errors, "；")
	for _, want := range []string{"sha256 与 manifest 不一致", "可能被替换/损坏", sum, expected} {
		if !strings.Contains(joined, want) {
			t.Fatalf("错误信息缺少 %q: %s", want, joined)
		}
	}
	if !strings.Contains(res.Summary(), "自检未通过") {
		t.Fatalf("Summary 应表达未通过: %q", res.Summary())
	}
}

// TestCompareHashNoManifestValue manifest 未声明 sha256 → 不算错误，只给警告。
func TestCompareHashNoManifestValue(t *testing.T) {
	ok, errs, warns := CompareHash("aa", "")
	if !ok {
		t.Fatalf("未声明期望哈希时不应判失败: errs=%v", errs)
	}
	if len(errs) != 0 {
		t.Fatalf("未声明期望哈希时不应有错误: %v", errs)
	}
	if len(warns) == 0 || !strings.Contains(warns[0], "未声明") {
		t.Fatalf("应给出未声明 sha256 的警告: %v", warns)
	}

	ok, errs, _ = CompareHash("", "aa")
	if ok || len(errs) == 0 {
		t.Fatalf("实际哈希为空应判失败: ok=%v errs=%v", ok, errs)
	}
}

// TestBuildVerifyResultMergesWarnings 未签名 + 黑名单启用 → 允许下发但警告齐全（Errors 仍为空）。
func TestBuildVerifyResultMergesWarnings(t *testing.T) {
	sum := strings.Repeat("cd", 32)
	res := BuildVerifyResult(sum, sum, "", SignatureStatus{
		Checked:  true,
		Signed:   false,
		Warnings: []string{"驱动没有 Authenticode 签名"},
	}, BlocklistStatus{
		Enabled:  true,
		Reason:   "已启用：VulnerableDriverBlocklistEnable = 1",
		Warnings: []string{"StartService 报 1275（ERROR_DRIVER_BLOCKED）"},
	})

	if len(res.Errors) != 0 {
		t.Fatalf("未签名/黑名单只应告警不应报错: %v", res.Errors)
	}
	if !res.HashOK || res.Signed || !res.Blocklisted {
		t.Fatalf("字段回填有误: %+v", res)
	}
	joined := strings.Join(res.Warnings, "｜")
	for _, want := range []string{"没有 Authenticode 签名", "未通过 Authenticode 签名校验", "1275"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("警告缺少 %q: %s", want, joined)
		}
	}
	if !strings.Contains(res.Summary(), "存在风险") {
		t.Fatalf("Summary 应表达风险: %q", res.Summary())
	}
}

// TestBuildVerifyResultSignerMismatch manifest 人工标注的签名者与实测不一致 → 提示人工核对。
func TestBuildVerifyResultSignerMismatch(t *testing.T) {
	sum := strings.Repeat("ef", 32)
	res := BuildVerifyResult(sum, sum, "Contoso Ltd.", SignatureStatus{Checked: true, Signed: true, Signer: "Fabrikam Inc."}, BlocklistStatus{})
	if !strings.Contains(strings.Join(res.Warnings, "｜"), "不一致") {
		t.Fatalf("签名者不一致应给出警告: %v", res.Warnings)
	}

	// 简称/包含关系不应误报
	res = BuildVerifyResult(sum, sum, "Contoso", SignatureStatus{Checked: true, Signed: true, Signer: "Contoso Ltd."}, BlocklistStatus{})
	if len(res.Warnings) != 0 {
		t.Fatalf("包含关系不应告警: %v", res.Warnings)
	}
}

// TestVerifyResultJSONTags 校验接口/列表返回的 JSON 字段名（前端兼容性回归）。
func TestVerifyResultJSONTags(t *testing.T) {
	sum := strings.Repeat("12", 32)
	res := BuildVerifyResult(sum, sum, "Contoso", SignatureStatus{Checked: true, Signed: true, Signer: "Contoso Ltd."}, BlocklistStatus{Enabled: true, Reason: "已启用"})
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	for _, key := range []string{"sha256", "manifest_sha256", "hash_ok", "signed", "signature_checked", "signer", "blocklisted", "blocklist_reason"} {
		if _, ok := m[key]; !ok {
			t.Fatalf("JSON 缺少字段 %q: %s", key, raw)
		}
	}
	if m["signed"] != true {
		t.Fatalf("signed 应为布尔: %s", raw)
	}
}

// TestListCarriesVerify 列表结果必须带自检结论，且 sha256 与实际内容一致。
// 这里把签名校验换成桩函数：测试不依赖本机证书链，也不会触发联网吊销检查。
func TestListCarriesVerify(t *testing.T) {
	old := signatureCheck
	signatureCheck = func(string, []byte) SignatureStatus {
		return SignatureStatus{Checked: true, Signed: true, Signer: "Test Signer"}
	}
	defer func() { signatureCheck = old }()

	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd 失败: %v", err)
	}
	// SearchDirs() 会扫 CWD/drivers，切到临时目录避免受仓库真实驱动目录影响。
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir 失败: %v", err)
	}
	defer os.Chdir(prev)

	body := []byte("list-verify-bytes")
	if err := os.MkdirAll(filepath.Join(dir, "drivers"), 0o700); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "drivers", "demo.sys"), body, 0o600); err != nil {
		t.Fatalf("写入驱动失败: %v", err)
	}
	manifest := `{"drivers":[{"file":"demo.sys","name":"demo","purpose":"kill","sha256":"` + sha256Hex(body) + `"}]}`
	if err := os.WriteFile(filepath.Join(dir, "drivers", "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("写入 manifest 失败: %v", err)
	}

	var found bool
	for _, d := range List() {
		if d.File != "demo.sys" {
			continue
		}
		found = true
		if d.SHA256 != sha256Hex(body) {
			t.Fatalf("驱动 sha256 错误: %s", d.SHA256)
		}
		if d.Verify == nil {
			t.Fatal("List() 结果必须带 Verify 自检结论")
		}
		if !platformSupported {
			// 非 Windows 平台只返回「不做自检」的占位结论（其余字段为零值）。
			if len(d.Verify.Warnings) == 0 {
				t.Fatalf("非 Windows 平台应有占位警告: %+v", *d.Verify)
			}
			continue
		}
		if d.Verify.SHA256 != sha256Hex(body) || !d.Verify.HashOK || len(d.Verify.Errors) != 0 {
			t.Fatalf("自检结果异常: %+v", *d.Verify)
		}
		if !d.Verify.Signed || d.Verify.Signer != "Test Signer" {
			t.Fatalf("签名桩结果未合并: %+v", *d.Verify)
		}
	}
	if !found {
		t.Fatal("List() 未列出临时目录里的 demo.sys")
	}
}
