package api

import (
	"sync"
	"time"

	"toshell/internal/common/types"
	"toshell/internal/server/logging"
)

// 会话上下线广播去抖（ROADMAP P0-1）。
//
// 问题：TCP 链路闪断时，监听器先回调 onSessionDead（广播 session_offline），
// 植入端几十秒内自动重连注册，又回调 onSessionOnline（广播 session_online），
// 操作员就看到「离线 → 在线」闪一下，但会话其实从未真正失联；同时还会
// 触发一次重复的上线 webhook。
//
// 处理：offline 广播延迟 sessionOfflineGrace 秒，期间若会话恢复则整体取消——
// 全程不发任何事件；只有真正持续失联才广播离线。online 广播同样去重
// （已在线的会话重复注册不再重复广播），返回值表示「这是一次真实状态变化」，
// 调用方可据此决定要不要发上线通知。
var sessionOfflineGrace = 15 * time.Second

type sessionBroadcastState struct {
	mu sync.Mutex
	// online 当前已广播为在线的会话
	online map[string]bool
	// pendingOffline 已判定离线但还在观察窗内的会话 → 延迟广播定时器
	pendingOffline map[string]*time.Timer
	// lastChange 最近一次状态变化时间，用于定期清理长期不活跃的条目
	lastChange map[string]time.Time
}

func newSessionBroadcastState() *sessionBroadcastState {
	return &sessionBroadcastState{
		online:         make(map[string]bool),
		pendingOffline: make(map[string]*time.Timer),
		lastChange:     make(map[string]time.Time),
	}
}

// BroadcastSessionOnline 广播 session_online 事件（新会话上线/恢复在线）。
//
// 返回值：本次是否真的广播了（首次上线、或离线观察窗已过的复活）。
// 返回 false 表示会话本来就处于在线状态（重复注册/闪断重连），调用方
// 不应重复触发上线通知。
func (s *Server) BroadcastSessionOnline(info *types.SessionInfo) bool {
	if info == nil || info.ID == "" {
		return false
	}
	if s.wsHub == nil {
		// 没有 WS 客户端时仍允许通知逻辑继续（websocket hub 未初始化场景）
		return s.noteSessionOnlineOnly(info.ID)
	}

	state := s.sessionState()
	state.mu.Lock()
	// 观察窗内恢复：取消待发的离线广播，且不再补发在线（前端从未看到离线）
	if t, ok := state.pendingOffline[info.ID]; ok {
		t.Stop()
		delete(state.pendingOffline, info.ID)
		state.lastChange[info.ID] = time.Now()
		state.mu.Unlock()
		logging.Info("session", "Session %s 在离线观察窗内恢复，抑制 offline/online 抖动", info.ID)
		return false
	}
	wasOnline := state.online[info.ID]
	state.online[info.ID] = true
	state.lastChange[info.ID] = time.Now()
	state.pruneLocked()
	state.mu.Unlock()

	if wasOnline {
		// 已在线：重复注册/心跳恢复，不重复广播
		return false
	}

	s.wsHub.Broadcast(WSEvent{
		Type:    "session_online",
		Payload: sessionOnlinePayload(info),
	})
	return true
}

// noteSessionOnlineOnly 无 WS hub 时仅维护去重状态。
func (s *Server) noteSessionOnlineOnly(id string) bool {
	state := s.sessionState()
	state.mu.Lock()
	defer state.mu.Unlock()
	if t, ok := state.pendingOffline[id]; ok {
		t.Stop()
		delete(state.pendingOffline, id)
		state.lastChange[id] = time.Now()
		return false
	}
	wasOnline := state.online[id]
	state.online[id] = true
	state.lastChange[id] = time.Now()
	return !wasOnline
}

// BroadcastSessionOffline 广播 session_offline 事件（会话判定死亡）。
//
// 广播被延迟 sessionOfflineGrace：期间会话复活则不发任何事件（见
// BroadcastSessionOnline）。无返回值。
func (s *Server) BroadcastSessionOffline(sessionID string) {
	if sessionID == "" {
		return
	}

	state := s.sessionState()
	state.mu.Lock()
	if !state.online[sessionID] {
		// 从未广播过在线（或已广播离线）：无需延迟，也没有可抖动的状态
		if _, pending := state.pendingOffline[sessionID]; !pending {
			state.mu.Unlock()
			return
		}
	}
	if _, exists := state.pendingOffline[sessionID]; exists {
		// 已在观察窗内，保持原定时器（不延长，避免持续抖动导致永不下线）
		state.mu.Unlock()
		return
	}
	timer := time.AfterFunc(sessionOfflineGrace, func() {
		s.flushSessionOffline(sessionID)
	})
	state.pendingOffline[sessionID] = timer
	state.lastChange[sessionID] = time.Now()
	state.pruneLocked()
	state.mu.Unlock()

	logging.Info("session", "Session %s 判定离线，%s 后广播（观察窗内重连则静默）", sessionID, sessionOfflineGrace)
}

// flushSessionOffline 观察窗到期：真正广播离线。
func (s *Server) flushSessionOffline(sessionID string) {
	state := s.sessionState()
	state.mu.Lock()
	delete(state.pendingOffline, sessionID)
	wasOnline := state.online[sessionID]
	delete(state.online, sessionID)
	state.lastChange[sessionID] = time.Now()
	state.mu.Unlock()

	if !wasOnline || s.wsHub == nil {
		return
	}
	logging.Info("session", "Session %s 持续失联，广播 session_offline", sessionID)
	s.wsHub.Broadcast(WSEvent{
		Type: "session_offline",
		Payload: map[string]interface{}{
			"id":     sessionID,
			"status": "dead",
		},
	})
}

// ForgetSessionBroadcast 会话被显式删除时清理去抖状态，避免长期占用内存。
func (s *Server) ForgetSessionBroadcast(sessionID string) {
	state := s.sessionState()
	state.mu.Lock()
	defer state.mu.Unlock()
	if t, ok := state.pendingOffline[sessionID]; ok {
		t.Stop()
		delete(state.pendingOffline, sessionID)
	}
	delete(state.online, sessionID)
	delete(state.lastChange, sessionID)
}

// sessionState 惰性初始化去抖状态（Server 可能由测试直接构造）。
func (s *Server) sessionState() *sessionBroadcastState {
	s.broadcastOnce.Do(func() {
		if s.broadcastState == nil {
			s.broadcastState = newSessionBroadcastState()
		}
	})
	return s.broadcastState
}

// pruneLocked 清理长期无活动的条目（会话 ID 会随时间累积）。
// 调用方须持有 state.mu。
func (s *sessionBroadcastState) pruneLocked() {
	if len(s.lastChange) <= 512 {
		return
	}
	cutoff := time.Now().Add(-2 * time.Hour)
	for id, at := range s.lastChange {
		if at.Before(cutoff) {
			if t, ok := s.pendingOffline[id]; ok {
				t.Stop()
				delete(s.pendingOffline, id)
			}
			delete(s.online, id)
			delete(s.lastChange, id)
		}
	}
}

// PendingOfflineCount 返回观察窗内待广播离线的会话数（测试/诊断用）。
func (s *Server) PendingOfflineCount() int {
	state := s.sessionState()
	state.mu.Lock()
	defer state.mu.Unlock()
	return len(state.pendingOffline)
}
