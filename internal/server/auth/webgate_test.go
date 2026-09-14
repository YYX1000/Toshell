package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"toshell/internal/server/config"
)

func newTestAuth() *Auth {
	return New(&config.AuthConfig{
		Enabled:       true,
		JWTEnabled:    true,
		JWTKey:        "test-jwt-key",
		APIKeyEnabled: true,
		APIKeys:       []string{"test-api-key"},
	})
}

func newGate(t *testing.T, cfg WebGateConfig, a *Auth, body string) http.Handler {
	t.Helper()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	})
	exempt := []string{"/api/v1/implant/"}
	return a.WebGate(cfg, exempt, next)
}

func hashOf(t *testing.T, pw string) string {
	t.Helper()
	h, err := HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// 未启用防护时完全放行（升级不改变既有行为）。
func TestWebGateDisabledPassesThrough(t *testing.T) {
	a := newTestAuth()
	h := newGate(t, WebGateConfig{Enabled: false}, a, "console")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "console" {
		t.Fatalf("disabled gate should pass through, got %d %q", rec.Code, rec.Body.String())
	}
}

// 未认证 + disguise 模式：返回 404，且不得泄露任何应用内容。
func TestWebGateDisguiseReturns404(t *testing.T) {
	a := newTestAuth()
	h := newGate(t, WebGateConfig{Enabled: true, User: "ops", PasswordHash: hashOf(t, "supersecret"), Disguise: true}, a, "console")
	for _, p := range []string{"/", "/assets/index.js", "/api/v1/sessions"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: want 404 (disguise), got %d", p, rec.Code)
		}
		if rec.Body.String() == "console" {
			t.Errorf("%s: 未认证请求泄露了控制台内容", p)
		}
		if rec.Header().Get("WWW-Authenticate") != "" {
			t.Errorf("%s: disguise 模式不应返回认证挑战", p)
		}
	}
}

// 未认证 + basic 模式：返回 401 + WWW-Authenticate（浏览器可弹框）。
func TestWebGateBasicModeReturns401(t *testing.T) {
	a := newTestAuth()
	h := newGate(t, WebGateConfig{Enabled: true, User: "ops", PasswordHash: hashOf(t, "supersecret")}, a, "console")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Error("缺少 WWW-Authenticate 挑战头")
	}
}

// 正确 Basic 凭据放行；错误凭据拒绝。
func TestWebGateAcceptsValidBasicCredentials(t *testing.T) {
	a := newTestAuth()
	cfg := WebGateConfig{Enabled: true, User: "ops", PasswordHash: hashOf(t, "supersecret"), Disguise: true}
	h := newGate(t, cfg, a, "console")

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", BasicAuthHeader("ops", "supersecret"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "console" {
		t.Fatalf("正确凭据应放行, got %d %q", rec.Code, rec.Body.String())
	}

	for _, bad := range [][2]string{{"ops", "wrong"}, {"other", "supersecret"}} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Authorization", BasicAuthHeader(bad[0], bad[1]))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			t.Errorf("错误凭据 %v 不应放行", bad)
		}
	}
}

// 植入端路径必须豁免（否则会话直接失联）。
func TestWebGateExemptsImplantPaths(t *testing.T) {
	a := newTestAuth()
	h := newGate(t, WebGateConfig{Enabled: true, User: "ops", PasswordHash: hashOf(t, "supersecret"), Disguise: true}, a, "implant-ok")
	for _, p := range []string{"/api/v1/implant/register", "/api/v1/implant/heartbeat", "/api/v1/implant/payload/abc", "/api/v1/implant/uac/token123"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", p, nil))
		if rec.Code != http.StatusOK || rec.Body.String() != "implant-ok" {
			t.Errorf("%s 应被豁免, got %d %q", p, rec.Code, rec.Body.String())
		}
	}
}

// 已持有 API Key 的自动化客户端放行（不破坏脚本/AI/MCP 集成）。
func TestWebGateAllowsAPIKeyClients(t *testing.T) {
	a := newTestAuth()
	h := newGate(t, WebGateConfig{Enabled: true, User: "ops", PasswordHash: hashOf(t, "supersecret"), Disguise: true}, a, "api-ok")
	req := httptest.NewRequest("GET", "/api/v1/sessions", nil)
	req.Header.Set("X-API-Key", "test-api-key")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("有效 API Key 应放行, got %d", rec.Code)
	}
	// 无效 key 仍被拦截
	req2 := httptest.NewRequest("GET", "/api/v1/sessions", nil)
	req2.Header.Set("X-API-Key", "wrong-key")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code == http.StatusOK {
		t.Error("无效 API Key 不应放行")
	}
}

// 凭据不完整（缺密码哈希）时视为未启用，避免把自己锁在外面。
func TestWebGateIncompleteCredsDoesNotLockOut(t *testing.T) {
	a := newTestAuth()
	h := newGate(t, WebGateConfig{Enabled: true, User: "ops", PasswordHash: "", Disguise: true}, a, "console")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("凭据不完整时不应启用防护（否则自锁）, got %d", rec.Code)
	}
}

// 来源白名单：不在网段内即使凭据正确也拒绝；网段内放行。
func TestWebGateAllowCIDRs(t *testing.T) {
	a := newTestAuth()
	cfg := WebGateConfig{Enabled: true, User: "ops", PasswordHash: hashOf(t, "supersecret"), Disguise: true, AllowCIDRs: []string{"10.0.0.0/8"}}
	h := newGate(t, cfg, a, "console")

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.1.2.3:5555"
	req.Header.Set("Authorization", BasicAuthHeader("ops", "supersecret"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("白名单内来源应放行, got %d", rec.Code)
	}

	req2 := httptest.NewRequest("GET", "/", nil)
	req2.RemoteAddr = "192.0.2.9:5555"
	req2.Header.Set("Authorization", BasicAuthHeader("ops", "supersecret"))
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code == http.StatusOK {
		t.Error("白名单外来源不应放行")
	}
}

// 隐蔽入口 Cookie：disguise 模式下浏览器靠它在无认证框的情况下进入控制台。
func TestWebGateStealthCookie(t *testing.T) {
	a := newTestAuth()
	secret := "stealth-key-0123456789"
	cfg := WebGateConfig{
		Enabled: true, User: "ops", PasswordHash: hashOf(t, "supersecret"),
		Disguise: true, StealthKey: secret, StealthCookie: "tsh_gate",
	}
	h := newGate(t, cfg, a, "console")

	// 无 Cookie：404 伪装
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("无 Cookie 应 404, got %d", rec.Code)
	}

	// 带正确 Cookie：放行（静态资源同样适用）
	for _, p := range []string{"/", "/assets/index-abc.js", "/api/v1/sessions"} {
		req := httptest.NewRequest("GET", p, nil)
		req.AddCookie(&http.Cookie{Name: "tsh_gate", Value: GateCookieValue(secret)})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s 带入口 Cookie 应放行, got %d", p, rec.Code)
		}
	}

	// 伪造 Cookie：仍 404
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: "tsh_gate", Value: "deadbeef"})
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusNotFound {
		t.Errorf("伪造 Cookie 不应放行, got %d", rec2.Code)
	}
}

// 入口密钥比对：空密钥 / 错误密钥一律不通过。
func TestStealthKeyMatches(t *testing.T) {
	if StealthKeyMatches("", "x") {
		t.Error("空密钥不应匹配")
	}
	if StealthKeyMatches("secret", "") {
		t.Error("空输入不应匹配")
	}
	if !StealthKeyMatches("secret", "secret") {
		t.Error("正确密钥应匹配")
	}
	if StealthKeyMatches("secret", "secret2") {
		t.Error("错误密钥不应匹配")
	}
}
