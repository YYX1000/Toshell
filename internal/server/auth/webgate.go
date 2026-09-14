package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// ─── Web 控制台前置防护（防资产测绘 / 防未授权访问）────────────────────
//
// 背景：资产测绘引擎（Fofa/Quake/Hunter/ZoomEye 等）会主动扫描公网端口并抓取
// 标题/指纹，把 C2 控制台收录成可检索资产。本中间件在「控制台 + 管理 API」前
// 加一道门槛：
//   - 默认 disguise 模式：未认证一律返回 404，对外表现为「该端口没有服务」，
//     测绘抓不到任何 C2 特征；
//   - basic 模式：返回 401 + WWW-Authenticate，浏览器弹出认证框，便于日常使用。
//
// 关键约束（不做会直接导致业务失联）：
//   - /api/v1/implant/* 必须豁免：植入端注册/心跳/结果回传，以及「一条命令上线」
//     的免认证载荷下载与一次性 UAC 载荷，都不能被浏览器认证拦住；
//   - 已持有 API Key / JWT 的自动化客户端（脚本、AI、MCP）继续放行，
//     避免为了防测绘而破坏既有集成；
//   - C2 监听器端口是独立服务，不经过本中间件。

// WebGateConfig Web 防护配置。
type WebGateConfig struct {
	Enabled      bool
	User         string
	PasswordHash string
	// Disguise 为 true 时未认证返回 404；false 时返回 401 挑战。
	Disguise bool
	// AllowCIDRs 非空时，仅允许这些来源网段访问（即使凭据正确）。
	AllowCIDRs []string
	// TrustProxyHeaders 为 true 时按 X-Forwarded-For 取真实来源 IP。
	TrustProxyHeaders bool
	// StealthKey 隐蔽入口密钥：非空时，携带正确入口 Cookie 的请求直接放行。
	// disguise 模式不返回 401 挑战（浏览器不会弹认证框），且浏览器不会把 URL 里的
	// Basic 凭据带到 JS/CSS 子资源请求上，因此入口 Cookie 是浏览器进入控制台的通道。
	StealthKey string
	// StealthCookie 入口 Cookie 名（默认 tsh_gate）。
	StealthCookie string
}

// DefaultStealthCookie 入口 Cookie 的默认名称。
const DefaultStealthCookie = "tsh_gate"

// GateCookieName 返回生效的入口 Cookie 名。
func (c WebGateConfig) GateCookieName() string {
	if strings.TrimSpace(c.StealthCookie) != "" {
		return strings.TrimSpace(c.StealthCookie)
	}
	return DefaultStealthCookie
}

// GateCookieValue 由入口密钥派生入口 Cookie 值（sha256 摘要，
// 避免明文密钥出现在浏览器 Cookie 中）。
func GateCookieValue(stealthKey string) string {
	sum := sha256.Sum256([]byte("toshell-gate:" + stealthKey))
	return hex.EncodeToString(sum[:])
}

// StealthKeyMatches 常量时间比较入口密钥（空密钥视为未启用）。
func StealthKeyMatches(want, provided string) bool {
	if strings.TrimSpace(want) == "" || provided == "" {
		return false
	}
	return subtleCompare(want, provided)
}

// GenerateStealthKey 生成隐蔽入口密钥（hex，32 字节 → 64 字符）。
// 用 hex 而非 base64：base64 的 "+" "/" "=" 放进 URL 查询串会被解析坏
// （"+" 会被解码成空格），导致入口链接直接失效。
func GenerateStealthKey() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// Disabled 报告防护是否实际生效（未启用或凭据不完整时视为不生效）。
func (c WebGateConfig) Disabled() bool {
	if !c.Enabled {
		return true
	}
	if strings.TrimSpace(c.User) == "" || strings.TrimSpace(c.PasswordHash) == "" {
		return true
	}
	// 非 bcrypt 哈希视为配置错误：拒绝启用而不是降级为明文比较
	if !strings.HasPrefix(c.PasswordHash, "$2") {
		return true
	}
	return false
}

// WebGate 返回 Web 控制台前置防护中间件。
// exemptPrefixes 内的路径直接放行（如 /api/v1/implant/）。
func (a *Auth) WebGate(cfg WebGateConfig, exemptPrefixes []string, next http.Handler) http.Handler {
	if cfg.Disabled() {
		if cfg.Enabled {
			log.Printf("[ERROR] [web] basic auth 已开启但凭据不完整（用户名或 bcrypt 哈希缺失），防护未生效！" +
				" 请在 设置→安全 中填写用户名与新密码（密码将以 bcrypt 存储）")
		}
		return next
	}

	var allowNets []*net.IPNet
	for _, c := range cfg.AllowCIDRs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(c); err == nil {
			allowNets = append(allowNets, n)
			continue
		}
		if ip := net.ParseIP(c); ip != nil {
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			if _, n, err := net.ParseCIDR(ip.String() + "/" + strconv.Itoa(bits)); err == nil {
				allowNets = append(allowNets, n)
			}
			continue
		}
		log.Printf("[WARN] [web] 忽略非法 allow_cidrs 项: %q", c)
	}

	reject := func(w http.ResponseWriter, r *http.Request) {
		if cfg.Disguise {
			// 对外表现"无此服务"：不返回任何认证挑战，不泄露任何应用特征
			http.NotFound(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="restricted"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		for _, p := range exemptPrefixes {
			if strings.HasPrefix(path, p) {
				next.ServeHTTP(w, r)
				return
			}
		}

		// 来源白名单（可选）：不在允许网段的请求直接按未认证处理
		if len(allowNets) > 0 {
			ip := clientIP(r, cfg.TrustProxyHeaders)
			allowed := false
			for _, n := range allowNets {
				if n.Contains(ip) {
					allowed = true
					break
				}
			}
			if !allowed {
				reject(w, r)
				return
			}
		}

		// 0) 隐蔽入口 Cookie：disguise 模式下浏览器进入控制台的通道
		//    （由 /__gate?k=<stealth_key> 种下；静态资源与 API 请求会自动携带）
		if strings.TrimSpace(cfg.StealthKey) != "" {
			if ck, err := r.Cookie(cfg.GateCookieName()); err == nil && ck != nil {
				if subtleCompare(ck.Value, GateCookieValue(cfg.StealthKey)) {
					next.ServeHTTP(w, r)
					return
				}
			}
		}

		// 1) Basic 凭据
		if user, pass, ok := r.BasicAuth(); ok {
			if subtleCompare(user, cfg.User) &&
				bcrypt.CompareHashAndPassword([]byte(cfg.PasswordHash), []byte(pass)) == nil {
				next.ServeHTTP(w, r)
				return
			}
			reject(w, r)
			return
		}

		// 2) 已有凭据的自动化客户端放行：X-API-Key / Bearer JWT / ?token=JWT
		//    （防测绘不应破坏脚本、AI 副驾驶与 MCP 集成）
		if a != nil && a.Config != nil {
			if !a.Config.Enabled {
				next.ServeHTTP(w, r)
				return
			}
			if key := r.Header.Get("X-API-Key"); key != "" && a.Config.APIKeyEnabled && a.ValidateAPIKey(key) {
				next.ServeHTTP(w, r)
				return
			}
			if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
				if claims, err := a.ValidateToken(strings.TrimPrefix(h, "Bearer ")); err == nil && claims != nil {
					next.ServeHTTP(w, r)
					return
				}
			}
			if tk := r.URL.Query().Get("token"); tk != "" && a.Config.JWTEnabled {
				if claims, err := a.ValidateToken(tk); err == nil && claims != nil {
					next.ServeHTTP(w, r)
					return
				}
			}
		}

		reject(w, r)
	})
}

// clientIP 取客户端 IP（直连取 RemoteAddr；信任反代时优先 X-Forwarded-For 首段）。
func clientIP(r *http.Request, trustProxy bool) net.IP {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first := strings.TrimSpace(strings.Split(xff, ",")[0])
			if ip := net.ParseIP(first); ip != nil {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip
	}
	return net.IPv4zero
}

// subtleCompare 常量时间字符串比较（避免用户名枚举的时序侧信道）。
func subtleCompare(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// BasicAuthHeader 生成 Basic 认证头（供测试与文档示例使用）。
func BasicAuthHeader(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}
