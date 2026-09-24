package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// checkProblem 一条不变量违规。
type checkProblem struct {
	What string // 违反了哪条不变量
	Why  string // 为什么这是问题（可操作）
}

func (c checkProblem) String() string { return "[x] " + c.What + "\n    " + c.Why }

// cmdCheck 校验那些"不检查就会静默腐坏"的不变量。
//
// 这里每一条都对应一个真实踩过的坑或本次修掉的缺陷 —— 不是泛泛的 lint：
//   · 模板单源：曾靠两份人工同步的镜像，且发版门禁测的是不发货的那份（假绿灯）
//   · 生成物在库外：release/implant{,_c} 是生成物，误入库会让"改了源没同步"看起来正常
//   · 打包指向：三处打包实现必须都取唯一源，否则镜像会以别的形式回归
//   · 配置镜像：两份 server.yaml.example 也靠人工同步，同样没有门禁
//   · 文档链接：包内 README 的相对链接按包内布局解析，改错就是给用户死链
func cmdCheck(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("check 不接受参数，收到: %v", args)
	}
	p, err := newPaths()
	if err != nil {
		return err
	}

	var problems []checkProblem

	problems = append(problems, checkTemplateSource(p)...)
	problems = append(problems, checkGeneratedArtifacts(p)...)
	problems = append(problems, checkExampleConfigs(p)...)
	problems = append(problems, checkPackagingPointers(p)...)
	problems = append(problems, checkReleaseMatrix(p)...)
	problems = append(problems, checkViteConfigShadow(p)...)
	problems = append(problems, checkDocLinks(p)...)

	if len(problems) > 0 {
		fmt.Fprintf(errOut, "不变量校验未通过（%d 项）:\n\n", len(problems))
		for _, c := range problems {
			fmt.Fprintln(errOut, c)
		}
		return fmt.Errorf("check 失败")
	}
	ok("全部不变量通过")
	return nil
}

// ── 1. 模板唯一源自身完好 ──────────────────────────────────────────

func checkTemplateSource(p paths) []checkProblem {
	var out []checkProblem

	if !dirExists(p.templateSrc()) {
		return append(out, checkProblem{
			"植入端模板源不存在: " + relOrAbs(p.root, p.templateSrc()),
			"构建载荷会直接失败；确认仓库完整检出",
		})
	}
	// 服务端以"目录内存在 main.go"判定模板目录有效（resolveImplantTemplateDir）
	if !fileExists(filepath.Join(p.templateSrc(), "main.go")) {
		out = append(out, checkProblem{
			"模板源缺少 main.go: " + relOrAbs(p.root, p.templateSrc()),
			"服务端以 main.go 的存在判定模板目录是否有效，缺它会回退到别处或报错",
		})
	}
	// C 模板固定取 Go 模板目录的兄弟目录（builder.go / toolchain.go 用 ../implant_c 推导）
	if !fileExists(filepath.Join(p.templateSrcC(), "main.c")) {
		out = append(out, checkProblem{
			"C 模板缺失或不同级: " + relOrAbs(p.root, p.templateSrcC()) + "/main.c",
			"builder.go 与 toolchain.go 以 ../implant_c 推导 C 模板位置，两者必须同级",
		})
	}
	return out
}

// ── 2. 生成物必须在库外，且与源一致 ────────────────────────────────

func checkGeneratedArtifacts(p paths) []checkProblem {
	var out []checkProblem

	// release/implant{,_c} 是生成物。若被 git 跟踪，说明有人又把它入库了 ——
	// 那会让"改了源忘了同步"看起来完全正常（磁盘上是旧生成物，git 里却是干净状态）。
	for _, d := range []string{p.templateGen(), p.templateGenC()} {
		if !dirExists(d) {
			continue // 未生成不是错误：build / sync / package 会生成它
		}
		if tracked, err := gitTracks(p.root, d); err == nil && tracked {
			out = append(out, checkProblem{
				"生成物被 git 跟踪: " + relOrAbs(p.root, d),
				"该目录是构建期生成物，应保持 gitignore；入库会让「改了源没同步」看起来正常。" +
					"修法: git rm -r --cached " + relOrAbs(p.root, d),
			})
		}
	}

	// 生成物存在就必须与源逐字节一致
	if dirExists(p.templateGen()) {
		if src, gen, err := compareDirs(p.templateSrc(), p.templateGen()); err == nil {
			if msg := describeDirDiff(src, gen); msg != "" {
				out = append(out, checkProblem{
					"运行用的模板生成物与唯一源不一致: " + relOrAbs(p.root, p.templateGen()),
					msg + "。执行 `devtool sync` 重新同步（服务端读的是生成物，不是源）",
				})
			}
		}
	}
	return out
}

// describeDirDiff 描述 srcHashes 与 genHashes 的差异；无差异返回空串。
func describeDirDiff(srcHashes, genHashes map[string]string) string {
	var b strings.Builder
	var missing, extra, differs []string
	for _, k := range sortedKeys(srcHashes) {
		switch v, ok := genHashes[k]; {
		case !ok:
			missing = append(missing, k)
		case v != srcHashes[k]:
			differs = append(differs, k)
		}
	}
	for _, k := range sortedKeys(genHashes) {
		if _, ok := srcHashes[k]; !ok {
			extra = append(extra, k)
		}
	}
	if len(missing) > 0 {
		b.WriteString(fmt.Sprintf("源有而生成物缺 %d 个（如 %s）；", len(missing), firstN(missing, 3)))
	}
	if len(differs) > 0 {
		b.WriteString(fmt.Sprintf("内容不同 %d 个（如 %s）；", len(differs), firstN(differs, 3)))
	}
	if len(extra) > 0 {
		b.WriteString(fmt.Sprintf("生成物多出 %d 个（如 %s）；", len(extra), firstN(extra, 3)))
	}
	return strings.TrimSuffix(b.String(), "；")
}

// ── 3. 两份 example 配置必须一致 ───────────────────────────────────

// 它们是一对靠人工同步的镜像（configs/ 是仓库根，release/configs/ 供 Toshell.* 本地起服务用），
// 目前没有任何门禁 —— 与模板镜像同一类风险。
func checkExampleConfigs(p paths) []checkProblem {
	a := p.join("configs", "server.yaml.example")
	b := p.join("release", "configs", "server.yaml.example")

	if !fileExists(a) || !fileExists(b) {
		return []checkProblem{{
			"示例配置缺失: " + relOrAbs(p.root, a) + " 或 " + relOrAbs(p.root, b),
			"两份都要在：configs/ 用于打包，release/configs/ 供本地 Toshell.* 生成配置",
		}}
	}
	ba, err1 := os.ReadFile(a)
	bb, err2 := os.ReadFile(b)
	if err1 != nil || err2 != nil {
		return []checkProblem{{"示例配置读取失败", fmt.Sprintf("%v / %v", err1, err2)}}
	}
	if string(ba) != string(bb) {
		return []checkProblem{{
			"两份 server.yaml.example 不一致",
			relOrAbs(p.root, a) + " 与 " + relOrAbs(p.root, b) + " 内容不同。" +
				"它们是人工同步的镜像，没有门禁 —— 请同步（打包用前者，本地起服务用后者）",
		}}
	}
	return nil
}

// ── 4. 打包实现必须都取唯一源（防镜像以别的形式回归）────────────────

// 打包与门禁都不能回到生成物上：
//   · .github/workflows/release.yml —— 必须走 `cmd/devtool package`（本地与 CI 同一实现），
//     且不得直接引用 release/implant（那是生成物）
//   · scripts/e2e_smoke.ps1 —— 必须直接指向唯一源 internal/server/builder/implant
//
// 为什么这条重要：改动前正是"门禁指向 internal/ 那份、而开发与发布包实际读 release/ 那份"，
// 于是门禁校验的是**不发货的那一份**，只改发货件不会让它变红（假绿灯）。
func checkPackagingPointers(p paths) []checkProblem {
	var out []checkProblem

	// ① CI 打包必须委托 devtool
	releaseYml := p.join(".github", "workflows", "release.yml")
	if b, err := os.ReadFile(releaseYml); err != nil {
		out = append(out, checkProblem{"读取失败: " + relOrAbs(p.root, releaseYml), err.Error()})
	} else {
		s := string(b)
		if genTemplateRe.MatchString(s) {
			out = append(out, checkProblem{
				"CI 打包引用了生成物 release/implant: " + relOrAbs(p.root, releaseYml),
				"生成物不能作为打包来源；打包应整体委托 `cmd/devtool package`（它取唯一源）",
			})
		}
		if !strings.Contains(s, "devtool package") {
			out = append(out, checkProblem{
				"CI 打包未走 cmd/devtool package: " + relOrAbs(p.root, releaseYml),
				"打包必须由 cmd/devtool 统一实现；退回内嵌脚本就会与本地实现漂移",
			})
		}
	}

	// ② 发版门禁必须直接指向唯一源
	smoke := p.join("scripts", "e2e_smoke.ps1")
	if b, err := os.ReadFile(smoke); err != nil {
		out = append(out, checkProblem{"读取失败: " + relOrAbs(p.root, smoke), err.Error()})
	} else {
		s := string(b)
		if genTemplateRe.MatchString(s) {
			out = append(out, checkProblem{
				"发版门禁引用了生成物 release/implant: " + relOrAbs(p.root, smoke),
				"门禁必须校验发货件的来源（唯一源），否则又会变成「门禁测的不是发货件」",
			})
		}
		if !strings.Contains(s, "internal/server/builder/implant") &&
			!strings.Contains(s, `internal\server\builder\implant`) {
			out = append(out, checkProblem{
				"发版门禁未指向模板唯一源: " + relOrAbs(p.root, smoke),
				"应把 implant.template_dir 指向 internal/server/builder/implant{,_c}",
			})
		}
	}
	return out
}

// genTemplateRe 匹配生成物路径 release/implant 与 release/implant_c。
// 用词边界排除 release/implants（复数）—— 那是 implant.output_dir 的载荷产物目录，与模板无关。
var genTemplateRe = regexp.MustCompile(`release[\\/]implant(_c)?\b`)

// ── 5. CI 平台矩阵必须与 devtool 的目标清单一致 ────────────────────

var matrixRowRe = regexp.MustCompile(
	`\{\s*goos:\s*(\w+),\s*goarch:\s*'?(\w+)'?,\s*ext:\s*'?([^,']*)'?,\s*zip:\s*([\w.\-]+),\s*deploy:\s*([\w./]+)\s*\}`)

func checkReleaseMatrix(p paths) []checkProblem {
	f := p.join(".github", "workflows", "release.yml")
	b, err := os.ReadFile(f)
	if err != nil {
		return []checkProblem{{"读取失败: " + relOrAbs(p.root, f), err.Error()}}
	}

	got := map[string]target{}
	for _, m := range matrixRowRe.FindAllStringSubmatch(string(b), -1) {
		t := target{GOOS: m[1], GOARCH: m[2], Ext: m[3], Zip: m[4], Deploy: m[5]}
		got[t.GOOS+"/"+t.GOARCH] = t
	}
	if len(got) == 0 {
		return []checkProblem{{
			"release.yml 的平台矩阵未解析到任何目标: " + relOrAbs(p.root, f),
			"格式可能与 devtool 的解析不符；矩阵是打包的来源，解析不到就等于没校验",
		}}
	}

	var out []checkProblem
	for _, want := range targets {
		key := want.GOOS + "/" + want.GOARCH
		g, ok := got[key]
		if !ok {
			out = append(out, checkProblem{
				"CI 矩阵缺少平台: " + key,
				"devtool 会打这个包，但 CI 不会 —— 发布时会少一个平台",
			})
			continue
		}
		if g.Zip != want.Zip || g.Ext != want.Ext || g.Deploy != want.Deploy {
			out = append(out, checkProblem{
				"CI 矩阵的 " + key + " 与 devtool 的目标定义不一致",
				fmt.Sprintf("ci: zip=%s ext=%q deploy=%s ｜ devtool: zip=%s ext=%q deploy=%s",
					g.Zip, g.Ext, g.Deploy, want.Zip, want.Ext, want.Deploy),
			})
		}
		delete(got, key)
	}
	for _, k := range sortedKeys(got) {
		out = append(out, checkProblem{
			"CI 矩阵多出平台: " + k,
			"devtool 不会打这个包，但 CI 会尝试 —— 两边定义已漂移",
		})
	}
	return out
}

// ── 6. web/vite.config.js 不得存在（会静默遮蔽 vite.config.ts）──────

// Vite 解析配置时优先加载 vite.config.js —— 若源目录里存在一个由 tsc 编译出来的
// vite.config.js，那么对 vite.config.ts 的任何修改都会被**静默忽略**，直到下次
// npm run build 重新生成 .js 才"突然生效"。
//
// 本仓库被这个坑过：代理目标写在 .ts 里改成 18081，实际仍按旧 .js 的 8081 走，
// 表现为"改了配置没反应 / 每次开发都得手动设 VITE_PROXY_TARGET"。
// 已把 tsconfig.node.json 改为 emitDeclarationOnly，从源头不再产出 .js；
// 这条检查负责兜住"万一又出现"（例如手动跑过别的 tsc 命令）。
func checkViteConfigShadow(p paths) []checkProblem {
	shadow := p.join("web", "vite.config.js")
	if !fileExists(shadow) {
		return nil
	}
	return []checkProblem{{
		"web/vite.config.js 存在，会遮蔽 vite.config.ts: " + relOrAbs(p.root, shadow),
		"Vite 优先加载 .js，对 .ts 的修改会被静默忽略（曾导致代理目标改动不生效）。" +
			"删掉它: rm web/vite.config.js；若反复出现，检查 web/tsconfig.node.json 是否缺 emitDeclarationOnly",
	}}
}

// ── 7. 文档相对链接 ────────────────────────────────────────────────

var mdLinkRe = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// 检查文档里的相对链接是否指向存在的文件。
//
// release/README.md 单独处理：它的链接是按**包内布局**写的（打包时被拷到包根），
// 所以在仓库 release/ 目录下解析必然失败 —— 这是刻意的，要按包内清单校验。
func checkDocLinks(p paths) []checkProblem {
	var out []checkProblem

	// 仓库布局的文档
	repoDocs := []string{
		p.join("README.md"), p.join("USAGE.md"), p.join("ROADMAP.md"),
		p.join("CHANGELOG.md"), p.join("SECURITY.md"), p.join("DISCLAIMER.md"),
		p.join("THIRD-PARTY-NOTICES.md"),
	}
	if ds, err := filepath.Glob(p.join("docs", "*.md")); err == nil {
		repoDocs = append(repoDocs, ds...)
	}
	for _, f := range repoDocs {
		out = append(out, checkLinksIn(f, func(target string) string {
			return filepath.Join(filepath.Dir(f), filepath.FromSlash(target))
		}, relOrAbs(p.root, f))...)
	}

	// 包内 README：目标按"解压后的包根"解析
	pkgReadme := p.join("release", "README.md")
	if fileExists(pkgReadme) {
		manifest := packageManifest(p)
		out = append(out, checkLinksIn(pkgReadme, func(target string) string {
			if manifest[target] {
				return "" // 标记为存在
			}
			return filepath.Join("/nonexistent-package-root", target)
		}, "release/README.md（按包内布局）")...)
	}
	return out
}

func checkLinksIn(file string, resolve func(string) string, label string) []checkProblem {
	var out []checkProblem

	b, err := os.ReadFile(file)
	if err != nil {
		return nil // 文件不存在不算链接错误
	}
	lines := strings.Split(string(b), "\n")
	for i, line := range lines {
		for _, m := range mdLinkRe.FindAllStringSubmatch(line, -1) {
			target := m[1]
			if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") ||
				strings.HasPrefix(target, "#") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			// 去掉锚点 / 查询
			if j := strings.IndexAny(target, "#?"); j >= 0 {
				target = target[:j]
			}
			if target == "" {
				continue
			}
			p := resolve(target)
			if p == "" {
				continue // 显式标记为存在（包内清单命中）
			}
			if _, err := os.Stat(p); err != nil {
				out = append(out, checkProblem{
					fmt.Sprintf("%s:%d 链接失效: %s", label, i+1, m[1]),
					"目标不存在；打包进包后同样是死链",
				})
			}
		}
	}
	return out
}

// packageManifest 返回"解压后包根下会存在"的路径集合，用于校验包内 README 的链接。
// 与 packageOne 的组装清单保持对应。
func packageManifest(p paths) map[string]bool {
	m := map[string]bool{
		"toserver": true, "toserver.exe": true,
		"configs/server.yaml.example": true,
		"README.md":                   true, "USAGE.md": true,
		"LICENSE": true, "DISCLAIMER.md": true, "THIRD-PARTY-NOTICES.md": true,
		"deploy.sh": true, "deploy.bat": true,
		"install.sh": true, "install.ps1": true,
		"data/av_fingerprints.json": true,
	}
	// docs/ 下的文件是整目录带进去的
	if err := filepath.WalkDir(p.join("docs"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if rel, err := filepath.Rel(p.root, path); err == nil {
			m[filepath.ToSlash(rel)] = true
		}
		return nil
	}); err != nil {
		// 忽略：docs 不存在时链接检查会自然报失效
		_ = err
	}
	return m
}

// ── git 辅助 ────────────────────────────────────────────────────────

func gitTracks(root, path string) (bool, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false, err
	}
	c := exec.Command("git", "-C", root, "ls-files", "--error-unmatch", filepath.ToSlash(rel))
	c.Stdout, c.Stderr = nil, nil
	return c.Run() == nil, nil
}

func firstN(xs []string, n int) string {
	if len(xs) > n {
		xs = xs[:n]
	}
	out := append([]string{}, xs...)
	sort.Strings(out)
	return strings.Join(out, ", ")
}
