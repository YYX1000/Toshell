// Command devtool 是 ToShell 的开发/发版管线工具。
//
// 存在的理由：同一条"前端 → webdist → 模板同步 → 服务端构建 → 组装发布包"的管线，
// 此前在仓库里实现了三遍，且跨三种语言：
//
//	· Toshell.sh / Toshell.ps1 / Toshell.bat（开发，三种语言各一遍）
//	· .github/workflows/release.yml 的 build + Package zip（CI，YAML 内嵌 bash）
//	· scripts/package_release.ps1（本地发版，PowerShell）
//
// 三份必须靠人工保持一致，而各自的编码约束又不同（.ps1 必须 UTF-8+BOM、.bat 必须
// GBK、.sh 必须 LF）—— 这批"编码地雷"本身就是重复的代价。收敛成一个 Go 程序后，
// 开发和 CI 跑的是同一段逻辑，且不再有编码分歧。
//
// 职责边界（刻意不做的）：**不接管 run / stop / logs**。那部分并非三处重复 ——
// 每个平台各有一份、互不冗余，其中"服务跑的是构建前的旧二进制"的判定还是踩过一次
// 真实事故才调对的（见提交 98696c5）。把它重写成 Go 是净风险而非去重。
// 因此 run/stop/logs 仍由 Toshell.* 各自实现，本工具只负责 build / sync / package / check。
//
// 用法：go run ./cmd/devtool <命令>
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// 输出通道。默认直连，在 GBK 控制台上会被换成"UTF-8 → 代码页"的转码写入器
// （见 initOutput 与 console_windows.go：cmd 默认 936，直接写 UTF-8 中文会乱码）。
var (
	out    io.Writer = os.Stdout
	errOut io.Writer = os.Stderr
)

// initOutput 决定是否需要把输出转码到控制台代码页。
//
// 仅在 stdout 是终端、且该终端代码页不是 UTF-8 时转码。输出被重定向或经管道传递时
// 不转码：此时消费方（文件、grep、CI 日志）按 UTF-8 读取，转成 GBK 会得到乱码。
func initOutput() {
	if !stdoutIsTerminal() || !consoleNeedsGBK() {
		return
	}
	out = transform.NewWriter(os.Stdout, simplifiedchinese.GBK.NewEncoder())
	errOut = transform.NewWriter(os.Stderr, simplifiedchinese.GBK.NewEncoder())
}

// stdoutIsTerminal 报告 stdout 是否为字符设备（终端 / 控制台）。
func stdoutIsTerminal() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

const usage = `ToShell 开发/发版管线工具

用法:
  devtool build              构建服务端（前端 → webdist → 模板同步 → go build → release/）
  devtool build --no-web     同上，但跳过前端构建（沿用 cmd/server/webdist 现有产物）
  devtool sync               仅同步植入端模板到 release/implant{,_c}/
  devtool package [选项]     组装发布包（zip + checksums.txt）
  devtool check              校验不变量（模板单源、生成物与源一致、配置镜像、
                             打包与发版门禁指向同一模板源、vite 配置遮蔽）
  devtool help               显示本帮助

package 选项:
  --target goos/goarch       只打一个平台，如 linux/amd64（默认 --all）
  --all                      打全部 6 个平台
  --version X.Y.Z            写入二进制的版本号（默认取最近的 git tag，取不到时为 dev）
  --out DIR                  输出目录（默认 release/release-zips）
  --keep-binaries            保留各平台中间二进制（默认打包后删除）

产物位置约定:
  服务端与模板生成物落在 release/ —— 那是"复刻发布包布局"的目录：服务端按
  【template_dir → TOSHELL_IMPLANT_TEMPLATE_DIR → exe 同目录 implant/ → …】回退解析
  模板，exe 放在 release/ 下即命中 exe 同目录 implant/，使本地开发与发布包走完全相同的
  解析路径，不会出现"本地能跑、发布包失效"。
  植入端模板的唯一可信源是 internal/server/builder/implant{,_c}/，release/ 下的两份是
  本工具生成的产物（已 gitignore），不要手工编辑。
`

func main() {
	initOutput()

	if len(os.Args) < 2 {
		fmt.Fprint(errOut, usage)
		os.Exit(2)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "build":
		err = cmdBuild(args)
	case "sync":
		err = cmdSync(args)
	case "package":
		err = cmdPackage(args)
	case "check":
		err = cmdCheck(args)
	case "help", "--help", "-h":
		fmt.Fprint(out, usage)
		return
	default:
		fmt.Fprintf(errOut, "未知命令: %s\n\n", cmd)
		fmt.Fprint(errOut, usage)
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(errOut, "\n[错误] %v\n", err)
		os.Exit(1)
	}
}

// repoRoot 从当前工作目录向上找 go.mod，返回仓库根。
// 这样无论在仓库根还是子目录执行都能定位到正确位置。
func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			// 确认是主模块而不是模板的嵌套 module（模板也有 go.mod）
			if _, err := os.Stat(filepath.Join(dir, "cmd", "server")); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("向上未找到仓库根（含 go.mod 与 cmd/server 的目录），当前: %s", wd)
		}
		dir = parent
	}
}

// 仓库内的关键路径。集中定义，避免各处散落字符串字面量。
type paths struct{ root string }

func newPaths() (paths, error) {
	r, err := repoRoot()
	if err != nil {
		return paths{}, err
	}
	return paths{root: r}, nil
}

func (p paths) join(elem ...string) string {
	return filepath.Join(append([]string{p.root}, elem...)...)
}

// 植入端模板唯一源（Go 与 C 必须同级：builder.go / toolchain.go 以 ../implant_c 推导 C 模板）
func (p paths) templateSrc() string  { return p.join("internal", "server", "builder", "implant") }
func (p paths) templateSrcC() string { return p.join("internal", "server", "builder", "implant_c") }

// 生成物（已 gitignore）：服务端在 exe 同目录找 implant/
func (p paths) templateGen() string  { return p.join("release", "implant") }
func (p paths) templateGenC() string { return p.join("release", "implant_c") }

func (p paths) releaseDir() string { return p.join("release") }
func (p paths) webDist() string    { return p.join("web", "dist") }
func (p paths) webEmbed() string   { return p.join("cmd", "server", "webdist") }
func (p paths) serverBin() string  { return p.join("release", "toserver") }
