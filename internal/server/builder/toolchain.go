package builder

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"toshell/internal/server/config"
	"toshell/internal/server/logging"
)

// ─── 构建工具链探测（mingw gcc / garble） ─────────────────────────────
//
// 历史实现只在少量硬编码目录 + 服务端进程的 PATH 里找 gcc，实测有两类误判：
//
//  1. 用户把 gcc 加进系统环境变量 PATH 之后，**已在运行的服务端进程仍持有旧环境**，
//     于是「gcc 明明存在却一直显示无 gcc」，必须重启服务端才能生效。
//  2. PATH 里只有 32 位 mingw（如 MSYS2 默认的 C:\msys64\mingw32\bin\gcc.exe）时，
//     请求 amd64 也会被它编译成 32 位 PE，架构静默不符。
//
// 现在按「配置 → 环境变量 → 服务端同目录便携工具链 → 常见安装目录 → 进程 PATH →
// Windows 注册表 PATH」逐级探测，并用 `gcc -dumpmachine` 真实取目标三元组校验架构，
// 结果带缓存（安装完工具链页面刷新即生效，无需重启）。

const (
	// gccProbeTTL 单个 gcc 路径的探测结果缓存时长（起进程有开销）
	gccProbeTTL = 5 * time.Minute
	// gccResolveTTL 按架构解析结果的缓存时长：短 TTL 让"装好工具链后刷新页面"即可生效
	gccResolveTTL = 30 * time.Second
	// garbleProbeTTL garble 兼容性探测结果缓存时长
	garbleProbeTTL = 10 * time.Minute
)

// gccTool 一个实测可用的 mingw gcc。
type gccTool struct {
	Path   string // 可执行文件绝对路径
	Triple string // -dumpmachine 结果，如 x86_64-w64-mingw32
	Source string // 来源说明（配置/环境变量/目录/PATH），用于提示运维
}

type gccProbeResult struct {
	triple string
	ok     bool
	at     time.Time
}

type gccResolveResult struct {
	tool    *gccTool
	warning string
	err     error
	at      time.Time
}

var (
	gccMu       sync.Mutex
	gccProbes   = map[string]gccProbeResult{}
	gccResolved = map[string]gccResolveResult{}
	garbleState struct {
		sync.Mutex
		checked bool
		ok      bool
		message string
		at      time.Time
	}
)

// gccCandidate 候选 gcc 路径及其来源。
type gccCandidate struct {
	path   string
	source string
}

// CStatus 返回 C 植入端可用性与给运维看的说明（模板 + mingw gcc）。
func (b *Builder) CStatus() (bool, string) {
	if !b.cTemplateAvailable() {
		return false, "未找到 C 植入端模板（implant_c/main.c）：请确认发行包完整，或检查 implant.template_dir 配置"
	}
	tool, warning, err := resolveGCC("amd64")
	if err != nil {
		return false, err.Error()
	}
	msg := fmt.Sprintf("C 植入端可用：%s（%s，来源：%s）", tool.Triple, tool.Path, tool.Source)
	if warning != "" {
		msg += "。" + warning
	}
	return true, msg
}

// cTemplateAvailable 检查 C 植入端模板是否存在（模板目录旁或服务端可执行文件旁）。
func (b *Builder) cTemplateAvailable() bool {
	if info, err := os.Stat(filepath.Join(b.implantDir, "..", "implant_c", "main.c")); err == nil && !info.IsDir() {
		return true
	}
	if exePath, err := os.Executable(); err == nil {
		if info, err2 := os.Stat(filepath.Join(filepath.Dir(exePath), "implant_c", "main.c")); err2 == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// resolveGCC 解析指定架构可用的 mingw gcc。
//
// 返回 warning 非空表示「没有架构完全匹配的 gcc，但存在可用的 mingw gcc」，
// 此时仍返回该 gcc（保证功能可用），由调用方把警告透出给用户——不静默改变架构。
func resolveGCC(arch string) (*gccTool, string, error) {
	key := normalizeArch(arch)

	gccMu.Lock()
	if r, ok := gccResolved[key]; ok && time.Since(r.at) < gccResolveTTL {
		gccMu.Unlock()
		return r.tool, r.warning, r.err
	}
	gccMu.Unlock()

	best, mismatched, probed := scanGCC(arch)

	var (
		tool    *gccTool
		warning string
		err     error
	)
	switch {
	case best != nil:
		tool = best
	case len(mismatched) > 0:
		// 只有非目标架构的 mingw gcc：仍然用它编译（32 位 PE 在 x64 Windows 上可运行），
		// 但必须明确告知，避免「请求 amd64 却拿到 32 位」这种静默错误。
		tool = mismatched[0]
		warning = fmt.Sprintf("未找到 %s 版 mingw gcc，将使用 %s（%s）编译，产物是 %s PE",
			key, tool.Triple, tool.Path, archOfTriple(tool.Triple))
	default:
		err = fmt.Errorf("%s", gccNotFoundMessage(probed))
	}

	gccMu.Lock()
	gccResolved[key] = gccResolveResult{tool: tool, warning: warning, err: err, at: time.Now()}
	gccMu.Unlock()

	if tool != nil {
		logging.Info("builder", "mingw gcc resolved for %s: %s (%s) via %s", key, tool.Path, tool.Triple, tool.Source)
	}
	return tool, warning, err
}

// scanGCC 扫描全部候选并实测，返回架构匹配项、非匹配的可用项、以及探测过的候选说明。
func scanGCC(arch string) (best *gccTool, mismatched []*gccTool, probed []string) {
	seen := map[string]bool{}
	for _, c := range collectGCCCandidates(arch) {
		key := strings.ToLower(filepath.Clean(c.path))
		if seen[key] {
			continue
		}
		seen[key] = true
		if info, err := os.Stat(c.path); err != nil || info.IsDir() {
			continue
		}
		probed = append(probed, fmt.Sprintf("%s（%s）", c.path, c.source))

		triple, ok := probeGCC(c.path)
		if !ok {
			continue
		}
		tool := &gccTool{Path: c.path, Triple: triple, Source: c.source}
		if tripleMatchesArch(triple, arch) {
			if best == nil {
				best = tool
			}
			continue
		}
		mismatched = append(mismatched, tool)
	}
	return best, mismatched, probed
}

// probeGCC 实测 gcc 目标三元组：非 mingw 目标（cygwin/clang/linux gcc）一律拒绝，
// 因为它们无法直接产出链接 ws2_32/bcrypt 的 Windows PE。
func probeGCC(path string) (string, bool) {
	gccMu.Lock()
	if r, ok := gccProbes[path]; ok && time.Since(r.at) < gccProbeTTL {
		gccMu.Unlock()
		return r.triple, r.ok
	}
	gccMu.Unlock()

	out, err := exec.Command(path, "-dumpmachine").Output()
	triple := strings.TrimSpace(string(out))
	ok := err == nil && triple != "" && strings.Contains(strings.ToLower(triple), "mingw")

	gccMu.Lock()
	gccProbes[path] = gccProbeResult{triple: triple, ok: ok, at: time.Now()}
	gccMu.Unlock()
	return triple, ok
}

// collectGCCCandidates 按可靠性顺序收集候选 gcc 路径。
func collectGCCCandidates(arch string) []gccCandidate {
	bin := "gcc"
	if runtime.GOOS == "windows" {
		bin = "gcc.exe"
	}
	prefixed := archPrefixedGCCNames(arch, bin)

	var out []gccCandidate
	add := func(path, source string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		out = append(out, gccCandidate{path: path, source: source})
	}
	addDir := func(dir, source string) {
		if strings.TrimSpace(dir) == "" {
			return
		}
		add(filepath.Join(dir, prefixed[0]), source)
		add(filepath.Join(dir, bin), source)
	}

	// 1) 配置指定（server.yaml: builder.mingw_gcc_path）
	if cfg := config.Get(); cfg != nil {
		if p := strings.TrimSpace(cfg.Builder.MingwGCCPath); p != "" {
			if strings.ContainsAny(p, `/\`) || strings.HasSuffix(strings.ToLower(p), ".exe") {
				add(p, "配置 builder.mingw_gcc_path")
			} else if resolved, err := exec.LookPath(p); err == nil {
				add(resolved, "配置 builder.mingw_gcc_path")
			}
		}
	}

	// 2) 环境变量：显式指定 → CC → 安装根目录/MINGW_PREFIX/MSYSTEM
	for _, envName := range []string{"TOSHELL_MINGW_GCC", "MINGW_GCC"} {
		if v := strings.TrimSpace(os.Getenv(envName)); v != "" {
			if resolved, err := exec.LookPath(v); err == nil {
				add(resolved, "环境变量 "+envName)
			} else {
				add(v, "环境变量 "+envName)
			}
		}
	}
	if cc := strings.TrimSpace(os.Getenv("CC")); cc != "" {
		// CC 可能是 "gcc"、"x86_64-w64-mingw32-gcc"，也可能带参数
		fields := strings.Fields(cc)
		if len(fields) > 0 {
			cand := fields[0]
			if resolved, err := exec.LookPath(cand); err == nil {
				add(resolved, "环境变量 CC")
			} else {
				add(cand, "环境变量 CC")
			}
		}
	}
	for _, envName := range []string{"MINGW_HOME", "MINGW_PREFIX", "MSYSTEM_PREFIX", "MSYS2_ROOT", "MSYS2_PATH"} {
		base := strings.TrimSpace(os.Getenv(envName))
		if base == "" {
			continue
		}
		add(filepath.Join(base, bin), "环境变量 "+envName)
		add(filepath.Join(base, "bin", bin), "环境变量 "+envName)
		for _, sub := range []string{"mingw64", "ucrt64", "mingw32", "clang64"} {
			add(filepath.Join(base, sub, "bin", bin), "环境变量 "+envName)
		}
	}

	// 3) 服务端同目录的便携工具链（用户把 MinGW 解压到服务端旁即可"内置"）
	if exePath, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exePath)
		dirs := []string{
			filepath.Join(exeDir, "mingw64", "bin"),
			filepath.Join(exeDir, "mingw32", "bin"),
			filepath.Join(exeDir, "ucrt64", "bin"),
			filepath.Join(exeDir, "mingw", "bin"),
			filepath.Join(exeDir, "tools", "mingw64", "bin"),
			filepath.Join(exeDir, "tools", "mingw32", "bin"),
			filepath.Join(exeDir, "tools", "mingw", "bin"),
			filepath.Join(exeDir, "gcc", "bin"),
		}
		for _, d := range dirs {
			addDir(d, "服务端目录便携工具链")
		}
		for _, pattern := range []string{
			filepath.Join(exeDir, "tools", "*", "bin", bin),
			filepath.Join(exeDir, "tools", "*", "bin", prefixed[0]),
		} {
			for _, m := range globFiles(pattern) {
				add(m, "服务端 tools/ 便携工具链")
			}
		}
	}

	// 4) 常见安装目录（MSYS2 / TDM-GCC / Chocolatey / Scoop / Strawberry / LLVM）
	programFiles := os.Getenv("ProgramFiles")
	programFilesX86 := os.Getenv("ProgramFiles(x86)")
	programData := os.Getenv("ProgramData")
	userProfile := os.Getenv("USERPROFILE")
	knownDirs := []string{
		`C:\msys64\mingw64\bin`, `C:\msys64\ucrt64\bin`, `C:\msys64\mingw32\bin`,
		`C:\msys2\mingw64\bin`, `C:\msys2\ucrt64\bin`, `C:\msys2\mingw32\bin`,
		`C:\mingw64\bin`, `C:\mingw32\bin`,
		`C:\TDM-GCC-64\bin`, `C:\TDM-GCC-32\bin`,
		`C:\tools\mingw64\bin`, `C:\tools\mingw32\bin`, `C:\tools\msys64\mingw64\bin`,
		`C:\Strawberry\c\bin`,
		`C:\ProgramData\chocolatey\bin`,
		`C:\ProgramData\mingw64\mingw64\bin`,
		`C:\ProgramData\mingw32\mingw32\bin`,
	}
	if userProfile != "" {
		knownDirs = append(knownDirs,
			filepath.Join(userProfile, `scoop\apps\mingw\current\bin`),
			filepath.Join(userProfile, `scoop\shims`),
		)
	}
	for _, base := range []string{programFiles, programFilesX86} {
		if base == "" {
			continue
		}
		knownDirs = append(knownDirs, filepath.Join(base, `mingw-w64`))
		knownDirs = append(knownDirs, filepath.Join(base, `LLVM\bin`), filepath.Join(base, `msys64\mingw64\bin`))
	}
	_ = programData
	for _, d := range knownDirs {
		addDir(d, "常见安装目录")
	}
	// 带版本号的安装目录（C:\Program Files\mingw-w64\x86_64-8.1.0-posix-seh-rt_v6-rev0\mingw64\bin）
	for _, base := range []string{programFiles, programFilesX86} {
		if base == "" {
			continue
		}
		for _, pattern := range []string{
			filepath.Join(base, "mingw-w64", "*", "mingw64", "bin", bin),
			filepath.Join(base, "mingw-w64", "*", "mingw32", "bin", bin),
		} {
			for _, m := range globFiles(pattern) {
				add(m, "常见安装目录（带版本号）")
			}
		}
	}

	// 5) 进程 PATH：先按架构前缀名，再退到裸 gcc
	for _, name := range prefixed {
		if p, err := exec.LookPath(name); err == nil {
			add(p, "PATH")
		}
	}
	if p, err := exec.LookPath(bin); err == nil {
		add(p, "PATH")
	}

	// 6) Windows 注册表 PATH：服务端进程启动后才把 gcc 加进系统 PATH 的场景，
	//    进程环境是旧的，只有读注册表才能立刻发现。
	for _, dir := range windowsRegistryPathDirs() {
		add(filepath.Join(dir, prefixed[0]), "系统 PATH（注册表）")
		add(filepath.Join(dir, bin), "系统 PATH（注册表）")
	}

	return out
}

// archPrefixedGCCNames 返回架构对应的 mingw gcc 前缀名。
func archPrefixedGCCNames(arch, bin string) []string {
	switch normalizeArch(arch) {
	case "386":
		return []string{"i686-w64-mingw32-gcc" + extIfWindows(bin), bin}
	case "arm64":
		return []string{"aarch64-w64-mingw32-gcc" + extIfWindows(bin), bin}
	default:
		return []string{"x86_64-w64-mingw32-gcc" + extIfWindows(bin), bin}
	}
}

// tripleMatchesArch 判断 gcc 目标三元组是否匹配请求架构。
func tripleMatchesArch(triple, arch string) bool {
	prefix := map[string]string{
		"amd64": "x86_64",
		"386":   "i686",
		"arm64": "aarch64",
	}[normalizeArch(arch)]
	if prefix == "" {
		prefix = "x86_64"
	}
	return strings.HasPrefix(strings.ToLower(triple), prefix)
}

// archOfTriple 把三元组翻译成便于阅读的架构名。
func archOfTriple(triple string) string {
	t := strings.ToLower(triple)
	switch {
	case strings.HasPrefix(t, "x86_64"):
		return "64 位"
	case strings.HasPrefix(t, "i686"), strings.HasPrefix(t, "i586"):
		return "32 位"
	case strings.HasPrefix(t, "aarch64"):
		return "ARM64"
	}
	return triple
}

// normalizeArch 归一化架构名（x86 → 386 等）。
func normalizeArch(arch string) string {
	switch strings.ToLower(strings.TrimSpace(arch)) {
	case "", "x64", "x86_64", "amd64":
		return "amd64"
	case "x86", "i386", "i686", "386", "32":
		return "386"
	case "arm64", "aarch64":
		return "arm64"
	}
	return strings.ToLower(strings.TrimSpace(arch))
}

// gccNotFoundMessage 生成"找不到 mingw gcc"的排障提示：列出探测过的位置，
// 避免用户反复问"我明明装了 gcc 为什么说没有"。
func gccNotFoundMessage(probed []string) string {
	var b strings.Builder
	b.WriteString("未找到可用的 mingw-w64 gcc（C 植入端需要它编译）：")
	if len(probed) > 0 {
		b.WriteString("已探测但不可用的候选：")
		for i, p := range probed {
			if i >= 5 {
				b.WriteString(fmt.Sprintf(" 等 %d 处", len(probed)))
				break
			}
			if i > 0 {
				b.WriteString("、")
			}
			b.WriteString(p)
		}
		b.WriteString("。")
	}
	b.WriteString("已检查：builder.mingw_gcc_path 配置、环境变量 TOSHELL_MINGW_GCC/CC/MINGW_HOME/MSYS2_ROOT/MINGW_PREFIX、" +
		"服务端同目录便携工具链、常见安装目录、PATH 与注册表 PATH。")
	b.WriteString("修复：安装 MSYS2 后执行 `pacman -S mingw-w64-x86_64-gcc`（32 位用 mingw-w64-i686-gcc），" +
		"或把便携 MinGW 解压到服务端目录（如 ./mingw64/bin/gcc.exe），或设置环境变量 TOSHELL_MINGW_GCC 指向 gcc.exe。")
	return b.String()
}

// globFiles 展开 glob 并只返回存在的文件。
func globFiles(pattern string) []string {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && !info.IsDir() {
			out = append(out, m)
		}
	}
	return out
}

// windowsRegistryPathDirs 读取 Windows 注册表里的用户/系统 PATH（展开变量后按 ; 拆分）。
//
// 用途：用户「装完 gcc 并把 PATH 加进系统环境变量」后，已运行的服务端进程环境是旧的，
// 只看 os.Getenv("PATH") 会误判为「无 gcc」，必须重启服务端才能识别。读注册表即可立刻生效。
func windowsRegistryPathDirs() []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	queries := [][]string{
		{`HKCU\Environment`, "Path"},
		{`HKLM\SYSTEM\CurrentControlSet\Control\Session Manager\Environment`, "Path"},
	}
	var dirs []string
	for _, q := range queries {
		out, err := exec.Command("reg", "query", q[0], "/v", q[1]).Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimRight(line, "\r")
			idx := strings.Index(line, "REG_EXPAND_SZ")
			if idx < 0 {
				idx = strings.Index(line, "REG_SZ")
				if idx >= 0 {
					idx += len("REG_SZ")
				}
			} else {
				idx += len("REG_EXPAND_SZ")
			}
			if idx < 0 {
				continue
			}
			value := strings.TrimSpace(line[idx:])
			if value == "" {
				continue
			}
			// 注册表值里可能是 %SystemRoot% 这类变量
			value = os.ExpandEnv(value)
			for _, d := range strings.Split(value, ";") {
				d = strings.TrimSpace(d)
				if d != "" {
					dirs = append(dirs, d)
				}
			}
		}
	}
	return dirs
}

// ─── garble 可用性 ──────────────────────────────────────────────────

// GarbleStatus 返回 garble 是否**真的可用**及原因说明。
//
// 只做 exec.LookPath("garble") 是不够的：garble 每个大版本都要求特定以上的 Go 版本，
// 版本不匹配时 `garble version` 正常返回，但任何 `garble build` 都会立刻失败
// （实测 garble v0.16.0 要求 Go ≥ 1.26，而本机 go1.25.0 → 界面显示"可用"但构建必失败）。
// 这里用一次极小的真实构建做探测（不含编译产物，秒级返回）并缓存结果。
func (b *Builder) GarbleStatus() (bool, string) {
	garbleState.Lock()
	defer garbleState.Unlock()

	if garbleState.checked && time.Since(garbleState.at) < garbleProbeTTL {
		return garbleState.ok, garbleState.message
	}

	path, err := exec.LookPath("garble")
	if err != nil {
		garbleState.checked, garbleState.ok = true, false
		garbleState.message = "未安装 garble（安装：go install mvdan.cc/garble@latest）"
		garbleState.at = time.Now()
		return false, garbleState.message
	}

	ok, detail := probeGarble(path)
	garbleState.checked, garbleState.ok = true, ok
	garbleState.at = time.Now()
	if ok {
		garbleState.message = "garble 可用：" + path
	} else {
		garbleState.message = detail
		logging.Warn("builder", "garble 不可用：%s", detail)
	}
	return ok, garbleState.message
}

// probeGarble 用最小工程真跑一次 garble build，判断工具链是否兼容。
func probeGarble(garblePath string) (bool, string) {
	tmpDir, err := os.MkdirTemp("", "toshell-garble-probe-*")
	if err != nil {
		return false, fmt.Sprintf("garble 探测失败：%v", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module toshellgarbleprobe\n\ngo 1.20\n"), 0644); err != nil {
		return false, fmt.Sprintf("garble 探测失败：%v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		return false, fmt.Sprintf("garble 探测失败：%v", err)
	}

	cmd := exec.Command(garblePath, "-literals", "build", "-o", filepath.Join(tmpDir, "probe.bin"), ".")
	cmd.Dir = tmpDir
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if err == nil {
		return true, ""
	}

	// 提取 garble 自己给出的版本提示，原样透出给用户
	lower := strings.ToLower(output)
	if strings.Contains(lower, "too old") || strings.Contains(lower, "is required") {
		return false, "garble 与当前 Go 版本不兼容：" + firstLine(output) + "（请升级 Go 或改用 `go install mvdan.cc/garble@latest` 匹配的版本）"
	}
	return false, "garble 不可用：" + firstLine(output)
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return s
}
