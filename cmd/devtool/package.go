package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// target 一个发布目标。targets 是平台清单的唯一来源：CI 与本地都经
// `devtool package` 使用本表，CI 侧不再单独维护一份平台矩阵。
type target struct {
	GOOS   string
	GOARCH string
	Ext    string // windows 为 .exe，其余为空
	Deploy string // 随包的部署入口（release/deploy.{sh,bat}）
	Zip    string
}

var targets = []target{
	{"windows", "amd64", ".exe", "release/deploy.bat", "toshell-server-windows-amd64.zip"},
	{"windows", "386", ".exe", "release/deploy.bat", "toshell-server-windows-386.zip"},
	{"linux", "amd64", "", "release/deploy.sh", "toshell-server-linux-amd64.zip"},
	{"linux", "arm64", "", "release/deploy.sh", "toshell-server-linux-arm64.zip"},
	{"darwin", "amd64", "", "release/deploy.sh", "toshell-server-darwin-amd64.zip"},
	{"darwin", "arm64", "", "release/deploy.sh", "toshell-server-darwin-arm64.zip"},
}

// verifyLayoutEntries 打包后必须存在于 zip 内的路径，由 verifyZipLayout 校验。
var verifyLayoutEntries = []string{
	"implant/main.go",   // 服务端解析模板的命中判据
	"implant_c/main.c",  // 运行期编译 C 植入端要用（builder.go 以 ../implant_c 推导）
	"configs/server.yaml.example",
	"README.md",
	"USAGE.md",
	"LICENSE",
	"docs/EVASION.md",
	"docs/LOADERS.md",
}

type packageOpts struct {
	only    []target
	all     bool
	version string
	outDir  string
	keepBin bool
}

func parsePackageArgs(args []string) (packageOpts, error) {
	o := packageOpts{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s 缺少取值", a)
			}
			i++
			return args[i], nil
		}
		switch {
		case a == "--all":
			o.all = true
		case a == "--keep-binaries":
			o.keepBin = true
		case a == "--target":
			v, err := next()
			if err != nil {
				return o, err
			}
			t, err := findTarget(v)
			if err != nil {
				return o, err
			}
			o.only = append(o.only, t)
		case a == "--version":
			v, err := next()
			if err != nil {
				return o, err
			}
			o.version = v
		case a == "--out":
			v, err := next()
			if err != nil {
				return o, err
			}
			o.outDir = v
		default:
			return o, fmt.Errorf("未知参数: %s", a)
		}
	}
	return o, nil
}

func findTarget(s string) (target, error) {
	for _, t := range targets {
		if strings.EqualFold(t.GOOS+"/"+t.GOARCH, s) {
			return t, nil
		}
	}
	names := make([]string, 0, len(targets))
	for _, t := range targets {
		names = append(names, t.GOOS+"/"+t.GOARCH)
	}
	return target{}, fmt.Errorf("未知平台 %q，可选: %s", s, strings.Join(names, ", "))
}

func cmdPackage(args []string) error {
	opts, err := parsePackageArgs(args)
	if err != nil {
		return err
	}
	p, err := newPaths()
	if err != nil {
		return err
	}

	selected := opts.only
	if len(selected) == 0 {
		selected = targets
	}

	version := opts.version
	if version == "" {
		version = gitVersion(p.root)
	}
	outDir := opts.outDir
	if outDir == "" {
		outDir = p.join("release", "release-zips")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	// 打包前先同步模板：保证包内的 implant/ 与仓库唯一源一致（而不是磁盘上的旧生成物）
	if err := syncTemplate(p); err != nil {
		return err
	}

	// 暂存目录与中间二进制置于系统临时目录，不落在仓库内：仓库内暂存目录若与既有
	// 源码目录重名，打包时会把该目录一并收进 zip。
	tmp, err := os.MkdirTemp("", "toshell-pkg-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	info("打包 %d 个平台，版本 %s，输出 %s", len(selected), version, relOrAbs(p.root, outDir))

	for _, t := range selected {
		if err := packageOne(p, t, version, outDir, tmp, opts.keepBin); err != nil {
			return fmt.Errorf("[%s/%s] %w", t.GOOS, t.GOARCH, err)
		}
	}

	if err := writeChecksums(outDir); err != nil {
		return err
	}
	ok("打包完成: %s", relOrAbs(p.root, outDir))
	return nil
}

func packageOne(p paths, t target, version, outDir, tmp string, keepBin bool) error {
	info("[%s/%s] 编译服务端...", t.GOOS, t.GOARCH)

	binName := "toserver" + t.Ext
	binPath := filepath.Join(tmp, binName)

	ldflags := fmt.Sprintf("-s -w -X main.version=%s -X main.commit=%s -X main.buildTime=%s",
		version, gitShort(p.root), time.Now().UTC().Format("2006-01-02T15:04:05Z"))

	c := exec.Command("go", "build", "-tags", "webui", "-ldflags", ldflags, "-o", binPath, "./cmd/server")
	c.Dir = p.root
	c.Env = append(os.Environ(),
		"GOOS="+t.GOOS,
		"GOARCH="+t.GOARCH,
		"CGO_ENABLED=0", // 与 CI 一致：纯静态、无 cgo 依赖
	)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("go build 失败: %w", err)
	}

	// 目标平台就是当前运行平台时，执行产物校验自报版本。
	// 理由：-X main.version 只靠 ldflags 注入，漏写/写错不会被编译期发现，
	// 而"发布包版本与 tag 不符"是用户直接可见的问题（CI 原本有一步专门查这个）。
	// 交叉编译的目标无法在此执行，跳过 —— 那需要目标机。
	if t.GOOS == runtime.GOOS && t.GOARCH == runtime.GOARCH {
		if err := verifyVersion(binPath, version); err != nil {
			return err
		}
	}

	stage := filepath.Join(tmp, "stage-"+t.GOOS+"-"+t.GOARCH)
	if err := os.RemoveAll(stage); err != nil {
		return err
	}
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return err
	}

	// ── 组装包内容 ──
	type item struct{ src, dst string }
	fileItems := []item{
		{binPath, binName},
		{p.join("configs", "server.yaml.example"), "configs/server.yaml.example"},
		// 包内 README 用 release/README.md（包内视角），不是仓库根 README（含多条指向
		// 不随包分发文档的链接，进包即死链）
		{p.join("release", "README.md"), "README.md"},
		{p.join("USAGE.md"), "USAGE.md"},
		{p.join("LICENSE"), "LICENSE"},
		{p.join("DISCLAIMER.md"), "DISCLAIMER.md"},
		{p.join("THIRD-PARTY-NOTICES.md"), "THIRD-PARTY-NOTICES.md"},
		{p.join(t.Deploy), filepath.Base(t.Deploy)},
		{p.join("release", "install.sh"), "install.sh"},
		{p.join("release", "install.ps1"), "install.ps1"},
		{p.join("data", "av_fingerprints.json"), "data/av_fingerprints.json"},
	}
	dirItems := []item{
		{p.templateSrc(), "implant"},   // 唯一源，不是 release/implant 生成物
		{p.templateSrcC(), "implant_c"},
		{p.join("docs"), "docs"},
	}
	for _, it := range fileItems {
		if !fileExists(it.src) {
			return fmt.Errorf("打包缺件: %s", relOrAbs(p.root, it.src))
		}
		if err := copyFile(it.src, filepath.Join(stage, filepath.FromSlash(it.dst))); err != nil {
			return err
		}
	}
	for _, it := range dirItems {
		if !dirExists(it.src) {
			return fmt.Errorf("打包缺目录: %s", relOrAbs(p.root, it.src))
		}
		if err := copyDir(it.src, filepath.Join(stage, filepath.FromSlash(it.dst))); err != nil {
			return err
		}
	}
	// upx / drivers / plugins 若存在则一并打包（可选依赖）
	for _, d := range []string{"upx", "drivers", "plugins"} {
		src := p.join("release", d)
		if dirExists(src) {
			if err := copyDir(src, filepath.Join(stage, d)); err != nil {
				return err
			}
		}
	}
	// 执行位不在这里改：Windows 文件系统不保留它，改也没用。
	// 统一由 writeZip 写进 zip 条目头（见 target.execPaths）。

	// ── 打 zip ──
	zipPath := filepath.Join(outDir, t.Zip)
	execSet := make(map[string]bool, len(t.execPaths())+1)
	for k, v := range t.execPaths() {
		execSet[k] = v
	}
	if err := writeZip(stage, zipPath, execSet); err != nil {
		return err
	}

	// 自校验：包内必须有能直接跑起来的最小集合。本地打包与 CI 同样受此校验。
	if err := verifyZipLayout(zipPath); err != nil {
		return err
	}

	fi, err := os.Stat(zipPath)
	if err != nil {
		return err
	}
	ok("[%s/%s] → %s (%.0f KB)", t.GOOS, t.GOARCH, filepath.Base(zipPath), float64(fi.Size())/1024)

	if !keepBin {
		os.Remove(binPath)
	}
	return nil
}

// execPaths 返回包内必须带执行位的路径（stage 相对、斜杠分隔）。
//
// 为什么必须显式设置、不能依赖源文件模式：
//   - git 里这些文件全部记录为 100644 —— 连 release/install.sh 与 release/upx/linux-amd64/upx
//     也是（git ls-files -s 可验证）
//   - Windows 文件系统不保留执行位，FileInfoHeader 一律给出 0666
//
// 于是本地在 Windows 上打的 Linux 包会得到不可执行的 deploy.sh / toserver / upx。
// CI（ubuntu）用 chmod +x 兜住了 deploy.sh 与 toserver，却漏了 upx —— 而 builder.go 的
// exec.Command(b.upxPath, …) 是要真正执行它的，不可执行会让"upx 随包自带、自动探测"
// 在 Linux 包上直接落空。这里一次性列全。
func (t target) execPaths() map[string]bool {
	if t.GOOS == "windows" {
		return nil // Windows 不认执行位，.exe / .bat / .ps1 都无需设置
	}
	return map[string]bool{
		"toserver":   true,
		"deploy.sh":  true,
		"install.sh": true, // deploy.sh 运行时会自己 chmod，但让直接执行 ./install.sh 也可用
	}
}

// writeZip 把 dir 的**内容**打到 zip 根（不产生外层目录）。
// execSet 里的路径强制 0755；upx/ 下的 Unix 二进制（非 .exe）同样强制 0755，其余沿用 0644。
func writeZip(dir, out string, execSet map[string]bool) error {
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		fi, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := zip.FileInfoHeader(fi)
		if err != nil {
			return err
		}
		hdr.Name = relSlash
		hdr.Method = zip.Deflate

		// 执行位：显式名单，或 upx/ 下的 Unix 二进制
		isUpxUnixBin := strings.HasPrefix(relSlash, "upx/") && !strings.HasSuffix(relSlash, ".exe")
		if execSet[relSlash] || isUpxUnixBin {
			hdr.SetMode(0o755)
		} else {
			hdr.SetMode(0o644)
		}

		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(w, in)
		return err
	})
	if err != nil {
		zw.Close()
		return err
	}
	return zw.Close()
}

// verifyZipLayout 检查包内是否带上"能直接跑起来"的最小集合。
func verifyZipLayout(zipPath string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()

	have := map[string]bool{}
	for _, f := range zr.File {
		have[f.Name] = true
	}
	var missing []string
	for _, want := range verifyLayoutEntries {
		if !have[want] {
			missing = append(missing, want)
		}
	}
	// 二进制的名字随扩展名变化，单独判
	if !have["toserver"] && !have["toserver.exe"] {
		missing = append(missing, "toserver(.exe)")
	}
	if len(missing) > 0 {
		return fmt.Errorf("包 %s 缺少: %s", filepath.Base(zipPath), strings.Join(missing, ", "))
	}
	return nil
}

// writeChecksums 对输出目录内的所有 zip 求 sha256，按文件名排序写入 checksums.txt。
// 格式与 `sha256sum` 输出一致，便于用 sha256sum -c 校验。
func writeChecksums(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".zip") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var sb strings.Builder
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		fmt.Fprintf(&sb, "%s  %s\n", hex.EncodeToString(sum[:]), n)
	}
	out := filepath.Join(dir, "checksums.txt")
	if err := os.WriteFile(out, []byte(sb.String()), 0o644); err != nil {
		return err
	}
	ok("checksums: %s（%d 个包）", filepath.Base(out), len(names))
	return nil
}

// verifyVersion 执行产物并断言它自报的版本包含 want。
func verifyVersion(binPath, want string) error {
	out, err := exec.Command(binPath, "-version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("执行 %s -version 失败: %w（输出: %s）",
			filepath.Base(binPath), err, strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), want) {
		return fmt.Errorf("产物自报版本与请求不符：期望包含 %q，实际 %q",
			want, strings.TrimSpace(string(out)))
	}
	info("  %s 自报版本校验通过（含 %q）", filepath.Base(binPath), want)
	return nil
}

// gitVersion 取最近的 tag 作为版本号（去掉前缀 v）；取不到则回退 dev。
// CI 里由 tag 触发，等价于 release.yml 的 ${GITHUB_REF_NAME#v}。
func gitVersion(root string) string {
	out, err := exec.Command("git", "-C", root, "describe", "--tags", "--abbrev=0").Output()
	if err != nil {
		return "dev"
	}
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
}
