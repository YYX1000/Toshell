package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestSaveKeepsUnrelatedFields 守护「读-改-写」语义：
// Save() 只允许改动传入的 key，配置文件中其余字段（未被 SetDefault 声明过的，
// 如 auth.admin_password / auth.api_keys / listener.heartbeat_timeout）必须原样保留。
// 历史 bug：Save() 用 viper.WriteConfig() 序列化合并结果，把这类字段写丢，
// 导致改端口后重启被「首次启动自动生成密码」锁在门外。
func TestSaveKeepsUnrelatedFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.yaml")
	original := []byte(`
server:
    api_port: 18081
listener:
    port: 8080
    heartbeat_timeout: 60s
auth:
    enabled: true
    admin_username: admin
    admin_password: "$2a$10$abcdefghijklmnopqrstuv"
    api_keys: Qingshan@2026
    jwt_key: 1Fa9FroKzPaD6Bpn2t53ICRyhMBScAho7RJE9AkCFFw=
ai:
    enabled: true
    consent_mode: auto
    api_key: sk-secret-should-survive
`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	// 仅修改监听端口（模拟 Web 设置页保存）
	if err := Save(map[string]interface{}{"listener.port": 38080}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := yaml.Unmarshal(raw, &m); err != nil {
		t.Fatalf("written file is not valid yaml: %v\n%s", err, raw)
	}

	// 1) 改动生效
	got, _ := nestedGet(m, "listener.port")
	if toInt(got) != 38080 {
		t.Errorf("listener.port = %v, want 38080\nfile:\n%s", got, raw)
	}

	// 2) 未触碰的敏感字段必须仍在
	for _, k := range []string{
		"auth.admin_username",
		"auth.admin_password",
		"auth.api_keys",
		"auth.jwt_key",
		"listener.heartbeat_timeout",
		"ai.consent_mode",
		"ai.api_key",
		"server.api_port",
	} {
		if v, ok := nestedGet(m, k); !ok || v == nil || v == "" {
			t.Errorf("field %q lost after Save()\nfile:\n%s", k, raw)
		}
	}
}

func nestedGet(m map[string]interface{}, key string) (interface{}, bool) {
	cur := interface{}(m)
	for _, part := range strings.Split(key, ".") {
		mm, ok := cur.(map[string]interface{})
		if !ok {
			return nil, false
		}
		cur, ok = mm[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func toInt(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return -1
}
