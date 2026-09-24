package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type ShellController interface {
	OpenShell(sessionID string, shell string) error
	SendShellInput(sessionID string, data string) error
	CloseShell(sessionID string) error
}

func (s *Server) shellWebSocketHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	sessionID := vars["id"]

	fmt.Printf("[DEBUG] [shell] WebSocket connection request for session: %s\n", sessionID)

	sess, err := s.sessionMgr.Get(sessionID)
	if err != nil {
		fmt.Printf("[ERROR] [shell] Session not found: %s\n", sessionID)
		http.Error(w, `{"error":"Session not found"}`, http.StatusNotFound)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Printf("[ERROR] [shell] WebSocket upgrade failed: %v\n", err)
		return
	}
	defer conn.Close()

	fmt.Printf("[INFO] [shell] WebSocket connected for session: %s\n", sessionID)

	shellChan := make(chan []byte, 100)
	cwdChan := make(chan string, 10)
	done := make(chan struct{})
	// 每个 shell 连接用唯一 handler key（而非 sessionID）：
	// 同会话并发开多个 shell 时互不覆盖（此前按 sessionID 注册，
	// 第二个连接会覆盖第一个的 handler，且 defer 会把对方的也删掉）。
	handlerID := uuid.NewString()

	sess.AddShellOutputHandler(handlerID, func(data []byte) {
		select {
		case shellChan <- data:
		default:
		}
	})
	defer sess.RemoveShellOutputHandler(handlerID)

	sess.AddShellCWDHandler(handlerID, func(cwd string) {
		select {
		case cwdChan <- cwd:
		default:
		}
	})
	defer sess.RemoveShellCWDHandler(handlerID)

	controller, ok := s.listener.(ShellController)
	if !ok {
		fmt.Printf("[ERROR] [shell] Listener does not implement ShellController\n")
		conn.WriteMessage(1, []byte("[错误: 服务端不支持交互式Shell]"))
		return
	}

	if err := controller.OpenShell(sessionID, ""); err != nil {
		fmt.Printf("[ERROR] [shell] Failed to open shell: %v\n", err)
		conn.WriteMessage(1, []byte(fmt.Sprintf("[错误: 无法打开Shell - %v]", err)))
		return
	}
	// 关键语义：**WS 断开 = 真实关闭靶机上的 shell 进程**，不是前端假断开。
	// 所以前端切面板必须"只隐藏、不卸载"终端组件；一旦卸载就会走到这里把
	// 靶机上的 bash 杀掉，切回来只能重开一条新 shell（历史与状态全丢）。
	// 这条日志存在的意义就是把"前端假断开"和"后端真断开"在日志里区分开：
	//   - 只看到 "WebSocket read error/close"，没有本行 → 会话通道仍然保持；
	//   - 看到本行 → 靶机上的 shell 已被真实终止。
	defer func() {
		fmt.Printf("[INFO] [shell] WS closed -> tearing down remote shell for session: %s\n", sessionID)
		if err := controller.CloseShell(sessionID); err != nil {
			fmt.Printf("[WARN] [shell] CloseShell failed for session %s: %v\n", sessionID, err)
		}
	}()

	fmt.Printf("[INFO] [shell] Shell opened for session: %s\n", sessionID)
	conn.WriteMessage(1, []byte("[Shell已连接，等待输出...]"))

	// CWD 标记消息格式: \x00CWD\x00<目录>；前端据此更新文件浏览器当前目录
	const cwdMarker = "\x00CWD\x00"
	if current := sess.GetShellCWD(); current != "" {
		conn.WriteMessage(1, []byte(cwdMarker+current))
	}

	// 输出 writer goroutine：conn 关闭或 done 触发即退出，
	// 不再永久阻塞泄漏（此前 select 无退出分支，每次 shell 泄漏 1 goroutine）
	go func() {
		defer func() { recover() }() // conn 并发写 panic 兜底
		for {
			select {
			case <-done:
				return
			case data := <-shellChan:
				if err := conn.WriteMessage(1, data); err != nil {
					return
				}
			case cwd := <-cwdChan:
				if err := conn.WriteMessage(1, []byte(cwdMarker+cwd)); err != nil {
					return
				}
			case <-time.After(30 * time.Second):
				// 空闲保活探测：检测 conn 是否已死（WriteMessage 失败即退出）
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			}
		}
	}()
	defer close(done)

	// 输入通道是否处于"收发不出去"的状态：用于把每键盘一次的报错收敛成每次中断一条
	inputBroken := false

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			fmt.Printf("[INFO] [shell] WebSocket read error: %v\n", err)
			break
		}

		// 靶机链路中断期间（植入体在重连空窗里没有 writer），**每一次按键**都会让
		// SendShellInput 失败。原先是每次失败都往终端写一条且不带换行符，
		// 于是用户只敲了 6 个字符就糊出一整行 "[错误: no writer for session xxx]" ×6。
		// 改为"每次中断只提示一次"：首次失败给出原因，后续失败静默丢弃，
		// 链路恢复后再告知一次。Shell 进程本身不会因此丢失（植入体重连后照常可用）。
		//
		// 提示走 NOTICE 标记（前端拦截后显示在状态栏），而不是直接写进终端：
		// 这是系统级信息，属于 UI 外壳；写进终端会污染靶机会话的回滚缓冲，
		// 也会打乱本地光标与远端 readline 的对应关系。原始错误只留在服务端日志。
		if err := controller.SendShellInput(sessionID, string(msg)); err != nil {
			if !inputBroken {
				inputBroken = true
				fmt.Printf("[WARN] [shell] session %s 输入未送达: %v（后续失败不再重复提示）\n", sessionID, err)
				conn.WriteMessage(1, []byte(
					"\x00NOTICE\x00warn|输入未送达靶机 —— 植入体正在重连，这期间的按键会被丢弃；恢复后可继续输入（Shell 进程不会丢）"))
			}
			continue
		}
		if inputBroken {
			inputBroken = false
			fmt.Printf("[INFO] [shell] session %s 输入通道已恢复\n", sessionID)
			conn.WriteMessage(1, []byte("\x00NOTICE\x00info|靶机链路已恢复"))
		}
	}

	fmt.Printf("[INFO] [shell] WebSocket handler exiting for session: %s\n", sessionID)
}
