// Package webhook 提供会话上线事件的通知能力：将新会话上线的信息 POST 到
// 操作员自定义的 URL（企业微信/钉钉/飞书/Slack/Discord 等机器人的 webhook 接口）。
// 仅在上线（新会话注册）时触发，避免高频事件打扰。
//
// 各平台的消息体结构完全不同，必须按平台构造，否则机器人接口会返回
// 「参数错误」并静默丢弃消息（issue #7：飞书返回
// `{"code":19002,"msg":"params error, msg_type need"}`，因为通用 JSON 里既没有
// msg_type，content 也不是对象）。本包按 URL 自动识别平台并各发各的结构：
//
//	钉钉   oapi.dingtalk.com                     → {"msgtype":"markdown",...}（加签走 URL 参数）
//	飞书   open.feishu.cn / open.larksuite.com   → {"msg_type":"text","content":{"text":...}}（加签在 body 内）
//	企业微信 qyapi.weixin.qq.com                 → {"msgtype":"text","text":{"content":...}}
//	Slack  hooks.slack.com                       → {"text":...}
//	Discord discord.com/api/webhooks             → {"content":...}
//	其它   generic                               → {"content":..., event..., 会话字段...}
package webhook

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"toshell/internal/common/types"
	"toshell/internal/server/config"
	"toshell/internal/server/logging"
)

// platform 通知目标平台：决定消息体结构与加签方式。
type platform string

const (
	platformGeneric  platform = "generic"  // 通用 JSON
	platformDingTalk platform = "dingtalk" // 钉钉机器人
	platformFeishu   platform = "feishu"   // 飞书 / Lark 自定义机器人
	platformWeCom    platform = "wecom"    // 企业微信群机器人
	platformSlack    platform = "slack"    // Slack Incoming Webhook
	platformDiscord  platform = "discord"  // Discord Webhook
)

// Notifier 会话上线 webhook 通知器。
// 通知配置在每次发送时从 config.Get() 读取，设置 API 保存或配置文件
// 修改后可立即热生效，无需重启进程（New 的 cfg 仅作初始参考）。
type Notifier struct {
	cfg    *config.WebhookConfig
	client *http.Client
}

// New 创建通知器。cfg 为 nil 或未启用时，NotifyOnline 为空操作。
func New(cfg *config.WebhookConfig) *Notifier {
	return &Notifier{
		cfg: cfg,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// liveCfg 返回当前生效的 webhook 配置（热更新优先，回退初始值）。
func (n *Notifier) liveCfg() *config.WebhookConfig {
	if g := config.Get(); g != nil {
		return &g.Webhook
	}
	return n.cfg
}

// resolveFormat 决定消息格式：显式格式优先，auto（或空）按 URL 域名识别。
// 兼容旧配置值 dingtalk/generic，新增 feishu/wecom/slack/discord。
func resolveFormat(cfg *config.WebhookConfig) platform {
	if cfg == nil {
		return platformGeneric
	}
	switch f := strings.ToLower(strings.TrimSpace(cfg.Format)); f {
	case "dingtalk", "dingding":
		return platformDingTalk
	case "feishu", "lark", "飞书":
		return platformFeishu
	case "wecom", "wechat", "workweixin", "企业微信":
		return platformWeCom
	case "slack":
		return platformSlack
	case "discord":
		return platformDiscord
	case "generic", "json":
		return platformGeneric
	}
	return detectPlatform(cfg.URL)
}

// detectPlatform 按 URL 识别平台。
func detectPlatform(rawURL string) platform {
	u := strings.ToLower(strings.TrimSpace(rawURL))
	switch {
	case u == "":
		return platformGeneric
	case strings.Contains(u, "oapi.dingtalk.com"), strings.Contains(u, "dingtalk.com/robot"):
		return platformDingTalk
	// 飞书自定义机器人：open.feishu.cn/open-apis/bot/v2/hook/xxx；Lark 国际版同路径
	case strings.Contains(u, "open.feishu.cn"), strings.Contains(u, "open.larksuite.com"),
		strings.Contains(u, "/open-apis/bot/"):
		return platformFeishu
	case strings.Contains(u, "qyapi.weixin.qq.com"):
		return platformWeCom
	case strings.Contains(u, "hooks.slack.com"):
		return platformSlack
	case strings.Contains(u, "discord.com/api/webhooks"), strings.Contains(u, "discordapp.com/api/webhooks"):
		return platformDiscord
	}
	return platformGeneric
}

// NotifyOnline 在新会话上线时发送通知。仅在启用且仅上线通知时发送。
func (n *Notifier) NotifyOnline(sess *types.SessionInfo) {
	if n == nil || sess == nil {
		return
	}
	cfg := n.liveCfg()
	if cfg == nil || !cfg.Enabled || !cfg.OnlyOnline {
		return
	}
	if cfg.URL == "" {
		return
	}

	body := n.renderContent(sess)
	status, respBody, err := SendTo(cfg, body)
	if err != nil {
		logging.Warn("webhook", "POST %s failed: %v", cfg.URL, err)
		return
	}
	// HTTP 200 也可能是业务失败（飞书 code / 钉钉 errcode 非 0），必须判定响应体
	ok, reason := EvaluateResponse(resolveFormat(cfg), status, respBody)
	if !ok {
		logging.Warn("webhook", "通知发送失败(%s): %s | body=%s", resolveFormat(cfg), reason, truncate(respBody, 200))
		return
	}
	logging.Info("webhook", "session_online notification sent for %s (%s@%s)", sess.ID, sess.Username, sess.Hostname)
}

// SendResult 一次测试发送的结果（设置页"发送测试通知"展示用）。
type SendResult struct {
	Platform   string `json:"platform"`
	StatusCode int    `json:"status_code"`
	Response   string `json:"response"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
}

// SendTest 发送一条测试消息到指定 webhook（设置页"发送测试通知"按钮）。
// 除网络/HTTP 错误外都返回结果对象，由调用方按 ok 判定；平台业务错误
// （飞书 code≠0 / 钉钉、企业微信 errcode≠0）会填进 Error。
func SendTest(webhookURL, content, format, secret string) (*SendResult, error) {
	cfg := &config.WebhookConfig{URL: webhookURL, Content: content, Format: format, Secret: secret}
	if content == "" {
		content = "ToShell 测试通知: 配置生效 ✅"
	}
	status, respBody, err := SendTo(cfg, content)
	if err != nil {
		return nil, err
	}
	p := resolveFormat(cfg)
	ok, reason := EvaluateResponse(p, status, respBody)
	return &SendResult{
		Platform:   string(p),
		StatusCode: status,
		Response:   truncate(respBody, 300),
		OK:         ok,
		Error:      reason,
	}, nil
}

// SendTo 按平台构造消息体并发送，返回（HTTP 状态码, 响应体, 错误）。
func SendTo(cfg *config.WebhookConfig, content string) (int, string, error) {
	platform := resolveFormat(cfg)
	payload := buildPayload(cfg, platform, content)
	client := &http.Client{Timeout: 10 * time.Second}
	return postPayload(client, cfg, platform, payload)
}

// buildPayload 按平台构造请求体。
func buildPayload(cfg *config.WebhookConfig, p platform, content string) []byte {
	switch p {
	case platformDingTalk:
		// 钉钉 markdown
		p2, _ := json.Marshal(map[string]interface{}{
			"msgtype": "markdown",
			"markdown": map[string]interface{}{
				"title": "ToShell 通知",
				"text":  content,
			},
			"at": map[string]interface{}{"isAtAll": false},
		})
		return p2

	case platformFeishu:
		// 飞书自定义机器人：msg_type 必填，text 内容必须在 content.text 里
		body := map[string]interface{}{
			"msg_type": "text",
			"content":  map[string]interface{}{"text": content},
		}
		// 开启了"签名校验"的机器人需要 timestamp + sign
		if ts, sign := feishuSign(cfg.Secret); sign != "" {
			body["timestamp"] = ts
			body["sign"] = sign
		}
		p2, _ := json.Marshal(body)
		return p2

	case platformWeCom:
		// 企业微信群机器人：msgtype + text.content
		p2, _ := json.Marshal(map[string]interface{}{
			"msgtype": "text",
			"text":    map[string]interface{}{"content": content},
		})
		return p2

	case platformSlack:
		p2, _ := json.Marshal(map[string]interface{}{"text": content})
		return p2

	case platformDiscord:
		p2, _ := json.Marshal(map[string]interface{}{"content": content})
		return p2
	}

	// 通用 JSON：content + 平台无关字段（供自建接收端使用）
	payload := map[string]interface{}{
		"event":     "session_online",
		"content":   content,
		"timestamp": time.Now().Unix(),
	}
	p2, _ := json.Marshal(payload)
	return p2
}

// feishuSign 生成飞书自定义机器人签名。
// 算法：sign = base64(HMAC-SHA256(key = timestamp + "\n" + secret, data = ""))，
// timestamp 为秒级字符串，随消息体一起提交。secret 为空时返回空串（未开启签名校验）。
func feishuSign(secret string) (string, string) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return "", ""
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	h := hmac.New(sha256.New, []byte(ts+"\n"+secret))
	h.Write([]byte(""))
	return ts, base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// EvaluateResponse 判定平台业务响应是否成功。
//
// 这些平台失败时大多仍返回 HTTP 200，只在响应体里给错误码：
//
//	飞书       {"code":0,...}    失败如 {"code":19002,"msg":"params error, msg_type need"}
//	钉钉/企微  {"errcode":0,...} 失败如 {"errcode":310000,"errmsg":"keywords not in content"}
//	Slack      "ok" / 非 2xx
//
// 返回 (是否成功, 失败原因)。原因里带上平台原话，便于排查。
func EvaluateResponse(p platform, status int, body string) (bool, string) {
	if status < 200 || status >= 300 {
		return false, fmt.Sprintf("HTTP %d", status)
	}
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		// 多数自建接收端返回空体即成功
		return true, ""
	}

	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &obj); err == nil {
		// 飞书用 code
		if v, ok := obj["code"]; ok {
			if code := toInt(v); code != 0 {
				return false, fmt.Sprintf("飞书返回 code=%d msg=%s", code, str(obj["msg"]))
			}
		}
		// 钉钉/企业微信用 errcode
		if v, ok := obj["errcode"]; ok {
			if code := toInt(v); code != 0 {
				return false, fmt.Sprintf("返回 errcode=%d errmsg=%s", code, str(obj["errmsg"]))
			}
		}
		// Slack 兼容：{"ok":false,"error":"..."}
		if v, ok := obj["ok"]; ok {
			if b, isBool := v.(bool); isBool && !b {
				return false, fmt.Sprintf("平台返回 ok=false error=%s", str(obj["error"]))
			}
		}
		return true, ""
	}

	// 非 JSON：Slack 成功时返回纯文本 "ok"
	if strings.EqualFold(trimmed, "ok") {
		return true, ""
	}
	if p == platformSlack {
		return false, "Slack 返回: " + truncated(trimmed, 120)
	}
	return true, ""
}

func toInt(v interface{}) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(t))
		return n
	}
	return 0
}

// postPayload 发送请求体并返回（HTTP 状态码, 响应体, 错误）。
// 钉钉加签模式（Secret 非空）自动追加 timestamp+sign 查询参数；
// 飞书加签在请求体内（见 buildPayload）。
func postPayload(client *http.Client, cfg *config.WebhookConfig, p platform, payload []byte) (int, string, error) {
	target := cfg.URL
	if p == platformDingTalk && cfg.Secret != "" {
		var err error
		target, err = dingtalkSignedURL(cfg.URL, cfg.Secret)
		if err != nil {
			return 0, "", fmt.Errorf("钉钉加签 URL 构造失败: %v", err)
		}
	}

	req, err := http.NewRequest("POST", target, bytes.NewReader(payload))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ToShell/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), nil
}

// dingtalkSignedURL 为钉钉加签模式追加 timestamp 与 sign 参数。
// 签名算法：sign = base64(HMAC-SHA256(secret, timestamp+"\n"+secret))。
func dingtalkSignedURL(rawURL, secret string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(ts + "\n" + secret))
	q := u.Query()
	q.Set("timestamp", ts)
	q.Set("sign", base64.StdEncoding.EncodeToString(h.Sum(nil)))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// renderContent 渲染内容模板：替换 {session_id} {hostname} {username} {os} {arch} {remote_addr} {time} 占位符。
func (n *Notifier) renderContent(sess *types.SessionInfo) string {
	tpl := n.liveCfg().Content
	if tpl == "" {
		tpl = "新会话上线: {hostname} ({username}@{os}/{arch}) 来自 {remote_addr}"
	}
	r := strings.NewReplacer(
		"{session_id}", sess.ID,
		"{hostname}", sess.Hostname,
		"{username}", sess.Username,
		"{os}", sess.OS,
		"{arch}", sess.Arch,
		"{remote_addr}", sess.RemoteAddr,
		"{time}", time.Now().Format("2006-01-02 15:04:05"),
	)
	return r.Replace(tpl)
}

// String 返回通知器配置摘要（用于日志/调试）。
func (n *Notifier) String() string {
	if n == nil || n.cfg == nil || !n.cfg.Enabled {
		return "webhook disabled"
	}
	return fmt.Sprintf("webhook enabled -> %s (%s)", n.cfg.URL, resolveFormat(n.cfg))
}

func str(v interface{}) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%v", v)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// truncated 同 truncate（供 EvaluateResponse 使用，保留可读别名）。
func truncated(s string, n int) string { return truncate(s, n) }
