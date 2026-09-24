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

// cmdCheck 校验仓库必须始终满足的不变量。
//
// 这些不变量保证：服务端读取的内容与其唯一源一致、打包与发版门禁取同一来源、
// 仓库状态能如实反映"源是否已同步"。
// 任一条失效时都不会立即报错，而是在后续环节静默产生错误结果（例如发出内容有误的包），
// 因此必须以检查而非约定来维持：
//   · 模板唯一源完好；release/implant{,_c} 为生成物、不被 git 跟踪、且与源逐字节一致
//   · 两份 server.yaml.example 内容一致（打包用前者，本地起服务用后者）
//   · 打包与发版门禁指向同一模板源
//   · web/vite.config.js 不存在（它会遮蔽 vite.config.ts）
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
	problems = append(problems, checkViteConfigShadow(p)...)

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
		srcHashes, err1 := fileHashes(p.templateSrc())
		genHashes, err2 := fileHashes(p.templateGen())
		if err1 == nil && err2 == nil {
			if msg := describeDirDiff(srcHashes, genHashes); msg != "" {
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

// ── 4. 打包与门禁必须取同一模板源 ─────────────────────────────────

// 不变量：打包（.github/workflows/release.yml，经 cmd/devtool package）与发版门禁
// （scripts/e2e_smoke.ps1）取同一模板源 internal/server/builder/implant{,_c}，
// 且都不得引用生成物 release/implant。
//
// 违反后果：门禁校验的不是实际分发的产物，只改发货件不会使门禁失败。
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

// ── 5. web/vite.config.js 不得存在 ────────────────────────────────

// 不变量：web/ 下不存在 vite.config.js。
//
// Vite 解析配置时优先加载 vite.config.js。该文件若由 tsc 从 vite.config.ts 编译而来
// 并留在源目录，对 vite.config.ts 的修改将不会生效，直到下次 npm run build 重新生成 .js。
// tsconfig.node.json 已设 emitDeclarationOnly，从源头不再产出 .js；本检查用于兜住
// 其他途径（如手动执行 tsc）产生的同名文件。
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
