package api

import (
	"fmt"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"

	"toshell/internal/common/tunnel"
	"toshell/internal/common/types"
	"toshell/internal/server/session"
)

// stubShellListener 既满足 Server.listener 的 TaskPusher，又实现 ShellController；
// SendShellInput 可按需失败，用来复现"靶机链路中断（没有 writer）"。
type stubShellListener struct {
	mu   sync.Mutex
	fail bool
}

// setFail 由测试协程调用，SendShellInput 由服务端协程读取 —— 必须加锁。
func (s *stubShellListener) setFail(v bool) {
	s.mu.Lock()
	s.fail = v
	s.mu.Unlock()
}

func (s *stubShellListener) OpenShell(string, string) error { return nil }
func (s *stubShellListener) CloseShell(string) error        { return nil }
func (s *stubShellListener) SendShellInput(string, string) error {
	s.mu.Lock()
	fail := s.fail
	s.mu.Unlock()
	if fail {
		return fmt.Errorf("no writer for session deadbeef")
	}
	return nil
}
func (s *stubShellListener) PushTask(string, *types.TaskInfo) error { return nil }
func (s *stubShellListener) PushFileUpload(string, string, string, string, int64, uint64) error {
	return nil
}
func (s *stubShellListener) SendTunnelPacket(string, *tunnel.TunnelPacket) error { return nil }
func (s *stubShellListener) SendTunnelRaw(string, []byte) error                  { return nil }
func (s *stubShellListener) ListRelayNodes() []types.RelayNode                   { return nil }

// 回归（用户实测：在闪断的靶机上敲 6 个字符，终端糊出一整行
// "[错误: no writer for session xxx]" ×6，且没有换行符）。
// 靶机重连空窗里每次按键都会下发失败，但提示必须"每次中断只出现一条"。
func TestShellInputFailureReportedOncePerOutage(t *testing.T) {
	const sid = "test-shell-input-outage"
	mgr := session.New()
	_ = mgr.Remove(sid)
	t.Cleanup(func() { _ = mgr.Remove(sid) })
	if err := mgr.Add(&types.SessionInfo{ID: sid, Status: "active", LastSeen: time.Now()}); err != nil {
		t.Fatalf("add session: %v", err)
	}

	ctrl := &stubShellListener{fail: true}
	srv := &Server{sessionMgr: mgr, listener: ctrl}

	r := mux.NewRouter()
	r.HandleFunc("/sessions/{id}/shell", srv.shellWebSocketHandler).Methods("GET")
	ts := httptest.NewServer(r)
	defer ts.Close()

	// 本机回环连接在该环境下偶发被拦（首次 SYN 超时，实测约 1/3 概率），
	// 属环境抖动而非被测逻辑，故带超时重试；重试仍失败才判失败。
	dialer := &websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
		NetDial:          (&net.Dialer{Timeout: 3 * time.Second}).Dial,
	}
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/sessions/" + sid + "/shell"
	var conn *websocket.Conn
	var err error
	for attempt := 1; ; attempt++ {
		conn, _, err = dialer.Dial(wsURL, nil)
		if err == nil {
			break
		}
		if attempt >= 5 {
			t.Fatalf("dial 连续 %d 次失败: %v", attempt, err)
		}
		t.Logf("dial 第 %d 次失败（%v），重试", attempt, err)
		time.Sleep(200 * time.Millisecond)
	}
	defer conn.Close()

	// 单一后台读协程持续收帧。
	// 注意：gorilla/websocket 读失败后不允许再读（会 panic
	// "repeated read on failed websocket connection"），所以不能用
	// "设读超时→超时后重试"的写法；这里只读一次，按时间窗口从 channel 收集。
	frames := make(chan string, 256)
	go func() {
		defer close(frames)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			frames <- string(msg)
		}
	}()
	// collectUntilSub 收帧直到某帧包含 sub（最长 timeout），然后再多收 grace，
	// 以便把"多余的重复提示"也一并捕获。用固定时间窗会因边界竞争而不稳定。
	collectUntilSub := func(sub string, timeout, grace time.Duration) []string {
		var out []string
		hard := time.After(timeout)
		var settle <-chan time.Time
		for {
			select {
			case f, ok := <-frames:
				if !ok {
					return out
				}
				out = append(out, f)
				if strings.Contains(f, sub) && settle == nil {
					settle = time.After(grace)
				}
			case <-settle:
				return out
			case <-hard:
				return out
			}
		}
	}

	// 链路中断期间连敲 6 次 → 只应有一条中断提示（旧行为是 6 条）
	for i := 0; i < 6; i++ {
		if err := conn.WriteMessage(websocket.TextMessage, []byte("x")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if got := collectUntilSub("输入已丢弃", 5*time.Second, 400*time.Millisecond); countContains(got, "输入已丢弃") != 1 {
		t.Fatalf("链路中断提示应恰好 1 条，实际 %d 条。帧=%v",
			countContains(got, "输入已丢弃"), got)
	}

	// 链路恢复后应提示一次"已恢复"
	ctrl.setFail(false)
	if err := conn.WriteMessage(websocket.TextMessage, []byte("y")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := collectUntilSub("靶机连接已恢复", 5*time.Second, 400*time.Millisecond); countContains(got, "靶机连接已恢复") != 1 {
		t.Fatalf("恢复提示应恰好 1 条，实际 %d 条。帧=%v",
			countContains(got, "靶机连接已恢复"), got)
	}

	// 再次中断 = 新的一轮 → 应再提示一次（而不是从此永久静默）
	ctrl.setFail(true)
	for i := 0; i < 3; i++ {
		if err := conn.WriteMessage(websocket.TextMessage, []byte("z")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if got := collectUntilSub("输入已丢弃", 5*time.Second, 400*time.Millisecond); countContains(got, "输入已丢弃") != 1 {
		t.Fatalf("新一轮中断应恰好 1 条提示，实际 %d 条。帧=%v",
			countContains(got, "输入已丢弃"), got)
	}
}

func countContains(frames []string, sub string) int {
	n := 0
	for _, f := range frames {
		if strings.Contains(f, sub) {
			n++
		}
	}
	return n
}
