package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"toshell/internal/common/types"
	"toshell/internal/server/config"
)

func TestDetectPlatform(t *testing.T) {
	cases := map[string]platform{
		"https://oapi.dingtalk.com/robot/send?access_token=x":      platformDingTalk,
		"https://open.feishu.cn/open-apis/bot/v2/hook/abc-123":     platformFeishu,
		"https://open.larksuite.com/open-apis/bot/v2/hook/abc":     platformFeishu,
		"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abc": platformWeCom,
		"https://hooks.slack.com/services/T00/B00/xxx":             platformSlack,
		"https://discord.com/api/webhooks/123/abc":                 platformDiscord,
		"https://discordapp.com/api/webhooks/123/abc":              platformDiscord,
		"https://notify.internal.example.com/hook":                 platformGeneric,
		"": platformGeneric,
	}
	for url, want := range cases {
		if got := detectPlatform(url); got != want {
			t.Errorf("detectPlatform(%q) = %q, want %q", url, got, want)
		}
	}
}

func TestResolveFormatExplicitWins(t *testing.T) {
	cfg := &config.WebhookConfig{URL: "https://open.feishu.cn/open-apis/bot/v2/hook/x", Format: "slack"}
	if got := resolveFormat(cfg); got != platformSlack {
		t.Fatalf("explicit format should win, got %q", got)
	}
	cfg.Format = "飞书"
	if got := resolveFormat(cfg); got != platformFeishu {
		t.Fatalf("中文别名应识别为 feishu, got %q", got)
	}
	cfg.Format = ""
	if got := resolveFormat(cfg); got != platformFeishu {
		t.Fatalf("auto 应按 URL 识别为 feishu, got %q", got)
	}
}

// issue #7 的核心回归：飞书要求 msg_type + content 对象，缺了就报
// {"code":19002,"msg":"params error, msg_type need"}。
func TestBuildPayloadPerPlatform(t *testing.T) {
	body := "新会话上线: PC1 (admin)"

	feishu := decode(t, buildPayload(&config.WebhookConfig{}, platformFeishu, body))
	if feishu["msg_type"] != "text" {
		t.Fatalf("飞书缺少 msg_type: %v", feishu)
	}
	content, ok := feishu["content"].(map[string]interface{})
	if !ok || content["text"] != body {
		t.Fatalf("飞书 content 必须是含 text 的对象: %v", feishu["content"])
	}

	wecom := decode(t, buildPayload(&config.WebhookConfig{}, platformWeCom, body))
	if wecom["msgtype"] != "text" {
		t.Fatalf("企业微信缺少 msgtype: %v", wecom)
	}
	if txt, _ := wecom["text"].(map[string]interface{}); txt == nil || txt["content"] != body {
		t.Fatalf("企业微信 text.content 结构错误: %v", wecom)
	}

	slack := decode(t, buildPayload(&config.WebhookConfig{}, platformSlack, body))
	if slack["text"] != body {
		t.Fatalf("Slack payload 错误: %v", slack)
	}

	discord := decode(t, buildPayload(&config.WebhookConfig{}, platformDiscord, body))
	if discord["content"] != body {
		t.Fatalf("Discord payload 错误: %v", discord)
	}

	dingtalk := decode(t, buildPayload(&config.WebhookConfig{}, platformDingTalk, body))
	if dingtalk["msgtype"] != "markdown" {
		t.Fatalf("钉钉 payload 错误: %v", dingtalk)
	}
	if md, _ := dingtalk["markdown"].(map[string]interface{}); md == nil || md["text"] != body {
		t.Fatalf("钉钉 markdown.text 结构错误: %v", dingtalk)
	}

	generic := decode(t, buildPayload(&config.WebhookConfig{}, platformGeneric, body))
	if generic["content"] != body || generic["event"] != "session_online" {
		t.Fatalf("通用 payload 错误: %v", generic)
	}
}

func TestFeishuSign(t *testing.T) {
	// 未配置密钥时不带 timestamp/sign
	plain := decode(t, buildPayload(&config.WebhookConfig{}, platformFeishu, "x"))
	if _, ok := plain["sign"]; ok {
		t.Fatalf("未配置 secret 不应带 sign: %v", plain)
	}

	secret := "test-secret"
	signed := decode(t, buildPayload(&config.WebhookConfig{Secret: secret}, platformFeishu, "x"))
	ts, _ := signed["timestamp"].(string)
	sign, _ := signed["sign"].(string)
	if ts == "" || sign == "" {
		t.Fatalf("开启签名后必须带 timestamp/sign: %v", signed)
	}
	// 用官方算法复算：base64(HMAC-SHA256(key=timestamp+"\n"+secret, data=""))
	h := hmac.New(sha256.New, []byte(ts+"\n"+secret))
	h.Write([]byte(""))
	if want := base64.StdEncoding.EncodeToString(h.Sum(nil)); sign != want {
		t.Fatalf("飞书签名不符: got %s want %s", sign, want)
	}
}

// 假飞书：严格按飞书自定义机器人的契约校验请求体，缺 msg_type 就返回 19002
// （复现 issue #7 的现象），否则返回 code=0。
func fakeFeishu(t *testing.T, requireSign bool, secret string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]interface{}
		if err := json.Unmarshal(raw, &body); err != nil {
			w.Write([]byte(`{"code":19001,"msg":"params error, body need json"}`))
			return
		}
		msgType, _ := body["msg_type"].(string)
		if msgType == "" {
			w.Write([]byte(`{"code":19002,"msg":"params error, msg_type need"}`))
			return
		}
		content, ok := body["content"].(map[string]interface{})
		if !ok {
			w.Write([]byte(`{"code":19003,"msg":"params error, content need object"}`))
			return
		}
		if _, ok := content["text"]; !ok {
			w.Write([]byte(`{"code":19004,"msg":"params error, text need"}`))
			return
		}
		if requireSign {
			ts, _ := body["timestamp"].(string)
			sign, _ := body["sign"].(string)
			h := hmac.New(sha256.New, []byte(ts+"\n"+secret))
			h.Write([]byte(""))
			if want := base64.StdEncoding.EncodeToString(h.Sum(nil)); sign == "" || sign != want {
				w.Write([]byte(`{"code":19021,"msg":"sign match fail"}`))
				return
			}
		}
		w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
}

func TestSendTestFeishuSucceeds(t *testing.T) {
	srv := fakeFeishu(t, false, "")
	defer srv.Close()

	res, err := SendTest(srv.URL+"/open-apis/bot/v2/hook/fake", "ToShell 测试通知", "", "")
	if err != nil {
		t.Fatalf("SendTest error: %v", err)
	}
	if !res.OK {
		t.Fatalf("飞书通知应成功，实际 ok=%v error=%q response=%q", res.OK, res.Error, res.Response)
	}
	if res.Platform != "feishu" {
		t.Fatalf("platform = %q, want feishu", res.Platform)
	}
}

func TestSendTestFeishuSignedSucceeds(t *testing.T) {
	const secret = "s3cr3t"
	srv := fakeFeishu(t, true, secret)
	defer srv.Close()

	res, err := SendTest(srv.URL+"/open-apis/bot/v2/hook/fake", "带签名", "", secret)
	if err != nil {
		t.Fatalf("SendTest error: %v", err)
	}
	if !res.OK {
		t.Fatalf("带签名的飞书通知应成功: %+v", res)
	}
}

// NotifyOnline 走真实 HTTP：启用 + 仅上线时发送飞书结构化消息；关闭后不发送。
func TestNotifyOnlineSendsFeishuPayload(t *testing.T) {
	var (
		called int
		body   map[string]interface{}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer srv.Close()

	cfg := config.Get()
	old := cfg.Webhook
	defer func() { cfg.Webhook = old }()
	cfg.Webhook = config.WebhookConfig{
		Enabled:    true,
		URL:        srv.URL + "/open-apis/bot/v2/hook/fake",
		OnlyOnline: true,
		Content:    "上线: {hostname} {username}",
	}

	n := New(&cfg.Webhook)
	n.NotifyOnline(&types.SessionInfo{ID: "sess-1", Hostname: "PC1", Username: "admin", OS: "windows", Arch: "amd64", RemoteAddr: "10.0.0.5"})

	if called != 1 {
		t.Fatalf("应发送 1 次通知，实际 %d 次", called)
	}
	if body["msg_type"] != "text" {
		t.Fatalf("飞书通知必须带 msg_type: %v", body)
	}
	content, _ := body["content"].(map[string]interface{})
	if content == nil || !strings.Contains(str(content["text"]), "PC1") {
		t.Fatalf("通知内容应渲染模板变量: %v", body)
	}

	// 关闭开关后不应再发送
	called = 0
	cfg.Webhook.Enabled = false
	n.NotifyOnline(&types.SessionInfo{ID: "sess-2", Hostname: "PC2"})
	if called != 0 {
		t.Fatalf("未启用时不应发送，实际 %d 次", called)
	}
}

func TestEvaluateResponse(t *testing.T) {
	cases := []struct {
		name   string
		p      platform
		status int
		body   string
		wantOK bool
	}{
		{"飞书成功", platformFeishu, 200, `{"code":0,"msg":"success"}`, true},
		{"飞书缺 msg_type", platformFeishu, 200, `{"code":19002,"data":{},"msg":"params error, msg_type need"}`, false},
		{"钉钉成功", platformDingTalk, 200, `{"errcode":0,"errmsg":"ok"}`, true},
		{"钉钉关键词未命中", platformDingTalk, 200, `{"errcode":310000,"errmsg":"keywords not in content"}`, false},
		{"企业微信成功", platformWeCom, 200, `{"errcode":0,"errmsg":"ok"}`, true},
		{"企业微信限流", platformWeCom, 200, `{"errcode":45009,"errmsg":"api freq out of limit"}`, false},
		{"Slack 成功", platformSlack, 200, "ok", true},
		{"Slack 失败", platformSlack, 200, "invalid_payload", false},
		{"Slack ok=false", platformSlack, 200, `{"ok":false,"error":"channel_not_found"}`, false},
		{"HTTP 500", platformGeneric, 500, `{"error":"boom"}`, false},
		{"空响应体", platformGeneric, 204, "", true},
		{"字符串 code", platformFeishu, 200, `{"code":"19002"}`, false},
	}
	for _, c := range cases {
		ok, reason := EvaluateResponse(c.p, c.status, c.body)
		if ok != c.wantOK {
			t.Errorf("%s: ok=%v want %v (reason=%q)", c.name, ok, c.wantOK, reason)
		}
		if !ok && reason == "" {
			t.Errorf("%s: 失败必须给出原因", c.name)
		}
	}
}

// issue #7 现象复现：旧实现发通用 JSON，飞书回 19002；现在必须成功。
func TestIssue7RegressionOldGenericPayloadFails(t *testing.T) {
	srv := fakeFeishu(t, false, "")
	defer srv.Close()

	// 旧行为：通用 JSON（没有 msg_type）
	oldPayload := []byte(`{"content":"hello","event":"session_online"}`)
	req, _ := http.NewRequest("POST", srv.URL+"/open-apis/bot/v2/hook/fake", strings.NewReader(string(oldPayload)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "19002") {
		t.Fatalf("旧通用 payload 应当被飞书拒绝(19002)，实际: %s", body)
	}
	if ok, _ := EvaluateResponse(platformFeishu, 200, string(body)); ok {
		t.Fatal("19002 必须被判为失败")
	}

	// 新行为：按平台构造
	res, err := SendTest(srv.URL+"/open-apis/bot/v2/hook/fake", "hello", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("修复后飞书通知应成功: %+v", res)
	}
}

func decode(t *testing.T, raw []byte) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("payload 不是 JSON: %v (%s)", err, raw)
	}
	return m
}
