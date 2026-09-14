package builder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"toshell/internal/server/config"
)

func TestTripleMatchesArch(t *testing.T) {
	cases := []struct {
		triple string
		arch   string
		want   bool
	}{
		{"x86_64-w64-mingw32", "amd64", true},
		{"x86_64-w64-mingw32", "386", false},
		{"i686-w64-mingw32", "386", true},
		{"i686-w64-mingw32", "amd64", false},
		{"aarch64-w64-mingw32", "arm64", true},
		{"x86_64-w64-mingw32", "x64", true}, // 别名
		{"i686-w64-mingw32", "x86", true},   // 别名
		{"x86_64-w64-mingw32", "", true},    // 空 = 默认 amd64
	}
	for _, c := range cases {
		if got := tripleMatchesArch(c.triple, c.arch); got != c.want {
			t.Errorf("tripleMatchesArch(%q,%q) = %v, want %v", c.triple, c.arch, got, c.want)
		}
	}
}

func TestArchPrefixedGCCNames(t *testing.T) {
	names := archPrefixedGCCNames("amd64", "gcc.exe")
	if len(names) < 2 || !strings.HasPrefix(names[0], "x86_64-w64-mingw32-gcc") {
		t.Fatalf("amd64 names = %v", names)
	}
	if names[len(names)-1] != "gcc.exe" {
		t.Fatalf("last candidate should be the bare gcc name: %v", names)
	}
	if got := archPrefixedGCCNames("386", "gcc")[0]; got != "i686-w64-mingw32-gcc" {
		t.Fatalf("386 prefix = %q", got)
	}
	if got := archPrefixedGCCNames("arm64", "gcc")[0]; got != "aarch64-w64-mingw32-gcc" {
		t.Fatalf("arm64 prefix = %q", got)
	}
}

func TestNormalizeArchAndArchOfTriple(t *testing.T) {
	for in, want := range map[string]string{
		"": "amd64", "x64": "amd64", "amd64": "amd64", "x86_64": "amd64",
		"x86": "386", "i386": "386", "386": "386", "arm64": "arm64", "aarch64": "arm64",
	} {
		if got := normalizeArch(in); got != want {
			t.Errorf("normalizeArch(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"x86_64-w64-mingw32":  "64 位",
		"i686-w64-mingw32":    "32 位",
		"aarch64-w64-mingw32": "ARM64",
	} {
		if got := archOfTriple(in); got != want {
			t.Errorf("archOfTriple(%q) = %q, want %q", in, got, want)
		}
	}
}

// 环境变量与配置里的 gcc 路径必须进入候选列表（这是 issue #6 的核心诉求：
// gcc 明明在环境变量里，服务端却说没有）。
func TestCollectGCCCandidatesIncludesEnvAndConfig(t *testing.T) {
	t.Setenv("TOSHELL_MINGW_GCC", `D:\green\mingw64\bin\gcc.exe`)
	t.Setenv("MINGW_HOME", `D:\msys\root`)
	t.Setenv("CC", "x86_64-w64-mingw32-gcc")

	cfg := config.Get()
	old := cfg.Builder.MingwGCCPath
	cfg.Builder.MingwGCCPath = `D:\custom\gcc.exe`
	defer func() { cfg.Builder.MingwGCCPath = old }()

	cands := collectGCCCandidates("amd64")
	bySource := map[string][]string{}
	for _, c := range cands {
		bySource[c.source] = append(bySource[c.source], strings.ToLower(filepath.Clean(c.path)))
	}
	hasUnder := func(source, substr string) bool {
		for _, p := range bySource[source] {
			if strings.Contains(p, substr) {
				return true
			}
		}
		return false
	}

	if !hasUnder("环境变量 TOSHELL_MINGW_GCC", `d:\green\mingw64\bin\gcc.exe`) {
		t.Errorf("TOSHELL_MINGW_GCC candidate missing: %v", bySource)
	}
	if !hasUnder("环境变量 MINGW_HOME", "mingw64") || !hasUnder("环境变量 MINGW_HOME", "mingw32") {
		t.Errorf("MINGW_HOME derived candidates missing: %v", bySource)
	}
	if !hasUnder("配置 builder.mingw_gcc_path", `d:\custom\gcc.exe`) {
		t.Errorf("config candidate missing: %v", bySource)
	}
	// CC 指向不存在的文件时也要保留为候选（用户可能马上装好）
	found := false
	for _, c := range cands {
		if c.source == "环境变量 CC" {
			found = true
		}
	}
	if !found {
		t.Errorf("CC candidate missing: %v", bySource)
	}
}

// 服务端同目录的便携工具链必须被探测（等价于"内置 gcc"：把 MinGW 解压到服务端旁即可）。
func TestCollectGCCCandidatesIncludesPortableToolchain(t *testing.T) {
	exePath, err := os.Executable()
	if err != nil {
		t.Skip("no executable path")
	}
	exeDir := filepath.Dir(exePath)
	cands := collectGCCCandidates("amd64")

	wantPortable := strings.ToLower(filepath.Join(exeDir, "mingw64", "bin", "gcc.exe"))
	if !strings.Contains(strings.ToLower(wantPortable), "mingw64") {
		t.Fatalf("unexpected exe dir: %s", exeDir)
	}
	found := false
	for _, c := range cands {
		if c.source == "服务端目录便携工具链" && strings.Contains(strings.ToLower(filepath.Clean(c.path)), "mingw64") {
			found = true
		}
	}
	if !found {
		t.Errorf("portable mingw64 candidate missing (exe dir %s)", exeDir)
	}
}

func TestProbeGCCRejectsNonMingw(t *testing.T) {
	if triple, ok := probeGCC(filepath.Join(os.TempDir(), "definitely-not-a-gcc")); ok {
		t.Fatalf("bogus path must not probe ok (triple=%q)", triple)
	}
}

func TestGccNotFoundMessageMentionsFixes(t *testing.T) {
	msg := gccNotFoundMessage([]string{`C:\msys64\mingw32\bin\gcc.exe（常见安装目录）`})
	for _, want := range []string{"mingw-w64 gcc", "TOSHELL_MINGW_GCC", "builder.mingw_gcc_path", "注册表", "msys64"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q: %s", want, msg)
		}
	}
}

// 文档里承诺的探测顺序至少要有：配置 → 环境变量 → 便携目录 → 常见安装目录 → PATH。
func TestCollectGCCCandidatesSourceOrder(t *testing.T) {
	t.Setenv("TOSHELL_MINGW_GCC", `D:\green\gcc.exe`)
	cfg := config.Get()
	old := cfg.Builder.MingwGCCPath
	cfg.Builder.MingwGCCPath = `D:\custom\gcc.exe`
	defer func() { cfg.Builder.MingwGCCPath = old }()

	cands := collectGCCCandidates("amd64")
	idx := func(source string) int {
		for i, c := range cands {
			if c.source == source {
				return i
			}
		}
		return -1
	}
	cfgIdx, envIdx, portIdx, knownIdx := idx("配置 builder.mingw_gcc_path"), idx("环境变量 TOSHELL_MINGW_GCC"), idx("服务端目录便携工具链"), idx("常见安装目录")
	if cfgIdx < 0 || envIdx < 0 || portIdx < 0 || knownIdx < 0 {
		t.Fatalf("missing sources: cfg=%d env=%d portable=%d known=%d", cfgIdx, envIdx, portIdx, knownIdx)
	}
	if !(cfgIdx < envIdx && envIdx < portIdx && portIdx < knownIdx) {
		t.Errorf("unexpected source order: cfg=%d env=%d portable=%d known=%d", cfgIdx, envIdx, portIdx, knownIdx)
	}
}
