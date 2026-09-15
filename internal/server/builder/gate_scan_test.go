package builder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"toshell/internal/server/config"
)

// 构建标签直接决定哪些免杀/对抗代码进入载荷，改错很难在功能测试里暴露，
// 因此固定行为：默认**不带** evasionscan（主动反沙箱进程检测默认关闭）。
func TestBuildTagList(t *testing.T) {
	cases := []struct {
		transport   string
		profile     string
		evasionScan bool
		want        string
	}{
		{"tcp", "full", false, ""},
		{"tcp", "full", true, "evasionscan"},
		{"http", "full", false, "transport_http"},
		{"http", "full", true, "transport_http evasionscan"},
		{"http", "light", false, "transport_http light"},
		{"websocket", "light", true, "transport_ws light evasionscan"},
		{"mqtt", "full", false, "transport_mqtt"},
	}
	for _, c := range cases {
		if got := buildTagList(c.transport, c.profile, c.evasionScan); got != c.want {
			t.Errorf("buildTagList(%q,%q,%v) = %q, want %q", c.transport, c.profile, c.evasionScan, got, c.want)
		}
	}
	// 默认载荷（不勾选任何免杀选项）绝不能带 evasionscan。
	if strings.Contains(buildTagList("tcp", "full", false), "evasionscan") {
		t.Fatal("default build must not include evasionscan")
	}
}

// 启动随机延迟必须真的被渲染进 main.go（模板占位符 {{STARTUP_DELAY_MIN/MAX}}）。
// 历史问题：Web 端 BuildRequest 里声明了 startup_delay_min/max，服务端结构体却没有
// 这两个字段 → 请求里的值被静默丢弃，只能吃服务端配置（表现为"随机延迟上线配置无效"）。
func TestProcessTemplatesRendersStartupDelay(t *testing.T) {
	tmpDir := t.TempDir()
	tmpl := config.ImplantConfig{TemplateDir: "implant"}
	if err := newBuilder(&tmpl).copyImplantSource(tmpDir, "windows"); err != nil {
		t.Fatalf("copyImplantSource: %v", err)
	}

	opts := BuildOptions{
		OS: "windows", Arch: "amd64", Format: "exe", Protocol: "tcp",
		StartDelayMin: 37, StartDelayMax: 91,
	}
	b := newBuilder(&tmpl)
	if err := b.processTemplates(tmpDir, opts); err != nil {
		t.Fatalf("processTemplates: %v", err)
	}

	src, err := os.ReadFile(filepath.Join(tmpDir, "main.go"))
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	content := string(src)
	if !strings.Contains(content, "startupDelayMin = 37") {
		t.Errorf("startupDelayMin not rendered with requested value 37")
	}
	if !strings.Contains(content, "startupDelayMax = 91") {
		t.Errorf("startupDelayMax not rendered with requested value 91")
	}
	if strings.Contains(content, "{{STARTUP_DELAY") {
		t.Errorf("unreplaced startup delay placeholder left in template")
	}
}

// 默认植入端模板必须**不含**主动反沙箱进程检测（进程枚举 + 杀软进程名），
// 该逻辑只能在 -tags evasionscan 时编译进来（gate_scan_windows.go）。
func TestDefaultTemplateHasNoProcessScan(t *testing.T) {
	dir := "implant"
	scanFile := filepath.Join(dir, "gate_scan_windows.go")
	offFile := filepath.Join(dir, "gate_scan_off_windows.go")
	for _, f := range []string{scanFile, offFile} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("missing %s: %v", f, err)
		}
	}
	scanSrc, err := os.ReadFile(scanFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(scanSrc), "//go:build windows && evasionscan") {
		t.Error("gate_scan_windows.go must be gated behind the evasionscan tag")
	}
	offSrc, err := os.ReadFile(offFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(offSrc), "//go:build windows && !evasionscan") {
		t.Error("gate_scan_off_windows.go must be the default (no evasionscan) implementation")
	}
	// 默认实现里不允许出现进程枚举/杀软进程名。
	for _, bad := range []string{"CreateToolhelp32Snapshot", "360tray", "huorong", "Process32First"} {
		if strings.Contains(string(offSrc), bad) {
			t.Errorf("default gate stub must not contain %q", bad)
		}
	}
	// 主 gate 文件也不应再包含枚举逻辑（已移入带标签的文件）。
	// 注释里会提到这些名字（说明为什么默认关闭），因此只看去掉注释后的代码。
	mainSrc, err := os.ReadFile(filepath.Join(dir, "gate_windows.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"CreateToolhelp32Snapshot", "Process32First", "360tray", "huorong", "strings.Contains"} {
		if strings.Contains(stripLineComments(string(mainSrc)), bad) {
			t.Errorf("gate_windows.go must no longer call %q (moved to gate_scan_windows.go)", bad)
		}
	}
}

// stripLineComments 去掉整行注释与行尾 // 注释，便于对"代码"而不是"文档"做断言。
func stripLineComments(src string) string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		if idx := strings.Index(line, "//"); idx >= 0 {
			line = line[:idx]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
