package api

import (
	"sync"
	"time"

	"toshell/internal/server/logging"
)

// 屏幕流服务端限速与帧合并（ROADMAP P0.2）。
//
// 植入端按 fps 出帧，但网络抖动、参数被改、或同一会话被多个页面观看时，帧仍可能
// 超发；前端只需要「最新一帧」——旧帧堆积只会浪费 WS 带宽并拖慢渲染。
// 这里按会话记录最小出帧间隔，超出的帧直接丢弃（合并到下一帧），
// 并统计丢弃数用于诊断（日志中按分钟粒度输出一次）。
const screenFrameMinInterval = 100 * time.Millisecond // 硬上限：10fps

type screenFrameLimiter struct {
	mu       sync.Mutex
	min      map[string]time.Duration
	last     map[string]time.Time
	dropped  map[string]uint64
	reported map[string]uint64
}

func newScreenFrameLimiter() *screenFrameLimiter {
	return &screenFrameLimiter{
		min:      make(map[string]time.Duration),
		last:     make(map[string]time.Time),
		dropped:  make(map[string]uint64),
		reported: make(map[string]uint64),
	}
}

// SetFPS 记录会话期望帧率（由 screen-stream 启动参数决定）。
func (l *screenFrameLimiter) SetFPS(sessionID string, fps int) {
	if sessionID == "" {
		return
	}
	if fps <= 0 {
		fps = 2
	}
	interval := time.Second / time.Duration(fps)
	if interval < screenFrameMinInterval {
		interval = screenFrameMinInterval
	}
	l.mu.Lock()
	l.min[sessionID] = interval
	l.mu.Unlock()
}

// Allow 判断该帧是否应当下发；false 表示丢弃（合并）。
func (l *screenFrameLimiter) Allow(sessionID string) bool {
	if sessionID == "" {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	interval, ok := l.min[sessionID]
	if !ok {
		interval = time.Second / 2 // 未声明帧率时按 2fps 兜底
	}
	now := time.Now()
	if last, ok := l.last[sessionID]; ok && now.Sub(last) < interval {
		l.dropped[sessionID]++
		return false
	}
	l.last[sessionID] = now
	// 丢弃数达到 50 的整数倍时输出一次诊断，避免刷屏
	if d := l.dropped[sessionID]; d >= 50 && d/50 != l.reported[sessionID]/50 {
		l.reported[sessionID] = d
		go logging.Info("screen-stream", "session %s 帧限速生效：已合并丢弃 %d 帧（当前上限 %v/帧）", sessionID, d, interval)
	}
	return true
}

// Reset 清理会话的限速状态（流停止/会话删除时调用）。
func (l *screenFrameLimiter) Reset(sessionID string) {
	l.mu.Lock()
	delete(l.min, sessionID)
	delete(l.last, sessionID)
	delete(l.dropped, sessionID)
	delete(l.reported, sessionID)
	l.mu.Unlock()
}

// Dropped 返回已丢弃帧数（测试/诊断用）。
func (l *screenFrameLimiter) Dropped(sessionID string) uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dropped[sessionID]
}

func (s *Server) frameLimiter() *screenFrameLimiter {
	s.screenLimiterOnce.Do(func() {
		if s.screenLimiter == nil {
			s.screenLimiter = newScreenFrameLimiter()
		}
	})
	return s.screenLimiter
}
