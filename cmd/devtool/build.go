package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ── 构建 ────────────────────────────────────────────────────────────

type buildOpts struct {
	skipWeb bool
}

func parseBuildArgs(args []string) (buildOpts, error) {
	var o buildOpts
	for _, a := range args {
		switch a {
		case "--no-web":
			o.skipWeb = true
		default:
			return o, fmt.Errorf("未知参数: %s（可用: --no-web）", a)
		}
	}
	return o, nil
}

func cmdBuild(args []string) error {
	opts, err := parseBuildArgs(args)
	if err != nil {
		return err
	}
	p, err := newPaths()
	if err != nil {
		return err
	}

	info("构建服务端（根目录: %s）", p.root)
	info("  植入端不在此构建 —— 由服务端生成载荷时按需编译（go1.20 工具链）")

	if _, err := exec.LookPath("go"); err != nil {
		return fmt.Errorf("未找到 Go 工具链（需要 Go >= 1.25），请先安装")
	}

	// ① 前端 → webdist（嵌入目标）
	if !opts.skipWeb {
		if err := buildFrontend(p); err != nil {
			return err
		}
	} else {
		info("已指定 --no-web，跳过前端构建（沿用 %s 现有产物）", relOrAbs(p.root, p.webEmbed()))
	}

	// ② 植入端模板同步（唯一源 → release/）
	if err := syncTemplate(p); err != nil {
		return err
	}

	// ③ 服务端
	hasWebui := fileExists(filepath.Join(p.webEmbed(), "index.html"))
	tags := ""
	if hasWebui {
		tags = "webui"
	}
	commit := gitShort(p.root)
	buildTime := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	ldflags := fmt.Sprintf("-s -w -X main.commit=%s -X main.buildTime=%s", commit, buildTime)

	buildArgs := []string{"build"}
	if tags != "" {
		buildArgs = append(buildArgs, "-tags", tags)
	}
	buildArgs = append(buildArgs, "-ldflags", ldflags, "-o", p.serverBin(), "./cmd/server")

	tagsDesc := ""
	if tags != "" {
		tagsDesc = "-tags " + tags + " "
	}
	info("编译服务端（go build %s-o %s）...", tagsDesc, relOrAbs(p.root, p.serverBin()))
	if err := runIn(p.root, "go", buildArgs...); err != nil {
		return fmt.Errorf("服务端构建失败: %w", err)
	}
	if !fileExists(p.serverBin()) {
		return fmt.Errorf("未生成产物: %s", p.serverBin())
	}
	ok("构建完成: %s（commit=%s，前端 %s）", relOrAbs(p.root, p.serverBin()), commit, assetID(p))
	return nil
}

// buildFrontend 跑 npm ci && npm run build 并把 web/dist 同步到 cmd/server/webdist。
//
// npm 缺失时**不会**构建成"纯 API 版"：是否带 -tags webui 取决于 webdist/index.html
// 是否存在，而 webdist 只在 clean 时删除。所以只要之前成功构建过一次前端，本次就会带
// 着**上一版**前端产物构建。把产物标识打出来，避免"界面怎么没变"却查不出原因。
func buildFrontend(p paths) error {
	if !fileExists(filepath.Join(p.root, "web", "package.json")) {
		info("未找到 web/package.json，跳过前端构建")
		return nil
	}
	if _, err := exec.LookPath("npm"); err != nil {
		warn("未找到 npm，跳过前端构建 —— 将沿用 %s 中已有的前端产物", relOrAbs(p.root, p.webEmbed()))
		warn("  本次将嵌入的前端产物标识: %s", assetID(p))
		warn("  若前端有改动，请安装 Node.js >= 20 后重新执行 build，否则界面仍是旧版本")
		return nil
	}

	info("构建前端（npm ci && npm run build）...")
	if err := runIn(filepath.Join(p.root, "web"), "npm", "ci"); err != nil {
		return fmt.Errorf("npm ci 失败: %w", err)
	}
	if err := runIn(filepath.Join(p.root, "web"), "npm", "run", "build"); err != nil {
		return fmt.Errorf("npm run build 失败: %w", err)
	}

	info("同步前端产物 → %s", relOrAbs(p.root, p.webEmbed()))
	if err := os.RemoveAll(p.webEmbed()); err != nil {
		return err
	}
	if err := os.MkdirAll(p.webEmbed(), 0o755); err != nil {
		return err
	}
	if err := copyDirContents(p.webDist(), p.webEmbed()); err != nil {
		return fmt.Errorf("同步 webdist 失败: %w", err)
	}
	ok("前端已同步，产物标识: %s", assetID(p))
	return nil
}

// ── 模板同步 ────────────────────────────────────────────────────────

func cmdSync(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("sync 不接受参数，收到: %v", args)
	}
	p, err := newPaths()
	if err != nil {
		return err
	}
	return syncTemplate(p)
}

// syncTemplate 把模板唯一源同步到 release/，复刻发布包布局。
//
// 必须逐字节复制：模板文件是 CRLF，任何文本模式改写都会破坏内容。
// implant_c 必须与 implant 同级（builder.go / toolchain.go 以 ../implant_c 推导）。
func syncTemplate(p paths) error {
	src, srcC := p.templateSrc(), p.templateSrcC()

	if !dirExists(src) {
		return fmt.Errorf("植入端模板源不存在: %s", src)
	}
	// 服务端以"目录内存在 main.go"判定模板目录是否有效（builder.go 的 resolveImplantTemplateDir）
	if !fileExists(filepath.Join(src, "main.go")) {
		return fmt.Errorf("模板源缺少 main.go（服务端以它判定目录是否有效）: %s", src)
	}

	info("同步植入端模板 → %s", relOrAbs(p.root, p.templateGen()))

	if err := os.RemoveAll(p.templateGen()); err != nil {
		return err
	}
	if err := os.RemoveAll(p.templateGenC()); err != nil {
		return err
	}
	if err := os.MkdirAll(p.releaseDir(), 0o755); err != nil {
		return err
	}
	if err := copyDir(src, p.templateGen()); err != nil {
		return fmt.Errorf("同步 implant 模板失败: %w", err)
	}
	if dirExists(srcC) {
		if err := copyDir(srcC, p.templateGenC()); err != nil {
			return fmt.Errorf("同步 implant_c 模板失败: %w", err)
		}
	}

	n, err := countFiles(p.templateGen())
	if err != nil {
		return err
	}
	ok("模板已同步: %d 个文件", n)
	return nil
}

// ── 辅助 ────────────────────────────────────────────────────────────

var assetRe = regexp.MustCompile(`assets/index-[A-Za-z0-9_-]+\.(js|css)`)

// assetID 读 cmd/server/webdist/index.html 里引用的 vite 产物标识（带内容哈希）。
func assetID(p paths) string {
	b, err := os.ReadFile(filepath.Join(p.webEmbed(), "index.html"))
	if err != nil {
		return "(无前端产物)"
	}
	if m := assetRe.FindString(string(b)); m != "" {
		return m
	}
	return "未知（index.html 中未找到 assets 引用）"
}

// copyDir 递归逐字节复制目录（保留权限位）。
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

// copyDirContents 把 src 的**内容**复制到 dst（不产生 dst/<src 基名> 这一层）。
func copyDirContents(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		t := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyDir(s, t); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(s, t); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// fileHashes 返回 dir 下所有文件的 相对路径 → SHA-256，用于逐文件比对两个目录。
func fileHashes(dir string) (map[string]string, error) {
	m := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
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
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		m[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	return m, err
}

func countFiles(dir string) (int, error) {
	n := 0
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			n++
		}
		return nil
	})
	return n, err
}

func gitShort(root string) string {
	out, err := exec.Command("git", "-C", root, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "dev"
	}
	return strings.TrimSpace(string(out))
}

// nowUTC 构建时间戳，与 CI 的 `date -u +'%Y-%m-%dT%H:%M:%SZ'` 同格式
// （注入 main.buildTime，服务端 -version 会打印它）。
func nowUTC() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

// compareDirs 返回两个目录的 相对路径→SHA-256 映射，供调用方自行比对。
func compareDirs(a, b string) (map[string]string, map[string]string, error) {
	ha, err := fileHashes(a)
	if err != nil {
		return nil, nil, err
	}
	hb, err := fileHashes(b)
	if err != nil {
		return nil, nil, err
	}
	return ha, hb, nil
}

func runIn(dir, name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Dir = dir
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	return c.Run()
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// relOrAbs 尽量给相对仓库根的短路径，便于阅读。
func relOrAbs(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(r, "..") {
		return filepath.ToSlash(r)
	}
	return p
}

// 三者都走 out（而非 fmt.Printf）—— 在 GBK 控制台上 out 是转码写入器，
// 直接写 stdout 会让中文变成乱码（见 main.go 的 initOutput）。
func info(format string, a ...any) { fmt.Fprintf(out, "[信息] "+format+"\n", a...) }
func ok(format string, a ...any)   { fmt.Fprintf(out, "[成功] "+format+"\n", a...) }
func warn(format string, a ...any) { fmt.Fprintf(out, "[警告] "+format+"\n", a...) }

// sortedKeys 便于稳定输出。
func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
