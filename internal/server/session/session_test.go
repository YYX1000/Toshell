package session

import (
	"testing"
	"time"

	"toshell/internal/common/types"
)

// withTimeoutConfig 临时设置判活基准与默认心跳间隔（测试用）。
func withTimeoutConfig(t *testing.T, base, defaultInterval time.Duration) {
	t.Helper()
	oldBase, oldInterval := HeartbeatTimeout, DefaultHeartbeatInterval
	HeartbeatTimeout = base
	DefaultHeartbeatInterval = defaultInterval
	t.Cleanup(func() {
		HeartbeatTimeout = oldBase
		DefaultHeartbeatInterval = oldInterval
	})
}

// P0-1：判活阈值必须留出余量，避免心跳间隔与超时几乎零余量时反复抖动。
func TestEffectiveTimeoutHasHeartbeatMargin(t *testing.T) {
	withTimeoutConfig(t, 60*time.Second, 5*time.Second)

	s := &Session{LastSeen: time.Now()}
	// 未采样到实测间隔：3×默认间隔=15s 小于基准，额外放宽到 2 倍基准，
	// 保证会话刚上线时首个心跳（可能刚好踩线）不会把它判死。
	if got := s.EffectiveTimeout(); got != 120*time.Second {
		t.Fatalf("无采样时阈值 = %v, want 120s（2 倍基准首周期保护）", got)
	}

	// 默认间隔与基准相当时，3 倍间隔直接生效
	withTimeoutConfig(t, 60*time.Second, 60*time.Second)
	s2 := &Session{LastSeen: time.Now()}
	if got := s2.EffectiveTimeout(); got != 180*time.Second {
		t.Fatalf("默认间隔 60s 时阈值 = %v, want 180s", got)
	}

	// 实测心跳间隔 62s（60s + jitter）→ 阈值至少 3×62 = 186s
	withTimeoutConfig(t, 60*time.Second, 5*time.Second)
	s.observedInterval = 62 * time.Second
	if got := s.EffectiveTimeout(); got != 186*time.Second {
		t.Fatalf("阈值 = %v, want 186s（3 倍心跳间隔）", got)
	}

	// 心跳很快时保持基准值（不因自适应而收紧到不可用）
	fast := &Session{LastSeen: time.Now(), observedInterval: 5 * time.Second}
	if got := fast.EffectiveTimeout(); got != 60*time.Second {
		t.Fatalf("快心跳阈值 = %v, want 60s", got)
	}
}

// 回归：心跳迟到几秒不再被误判离线（旧实现 60s 阈值 + 62s 心跳 = 必然误判）。
func TestHeartbeatJitterDoesNotKillSession(t *testing.T) {
	withTimeoutConfig(t, 60*time.Second, 5*time.Second)

	s := &Session{observedInterval: 62 * time.Second}
	now := time.Now()

	s.LastSeen = now.Add(-62 * time.Second)
	if !s.isAliveAt(now) {
		t.Fatal("62s 前心跳（间隔 62s）不应判离线")
	}
	s.LastSeen = now.Add(-170 * time.Second)
	if !s.isAliveAt(now) {
		t.Fatal("170s < 186s 阈值，不应判离线")
	}
	// 真正失联（超过 3 倍间隔）才判死
	s.LastSeen = now.Add(-200 * time.Second)
	if s.isAliveAt(now) {
		t.Fatal("超过 3 倍心跳间隔应判离线")
	}
}

// 忙期（长任务运行中）阈值再放宽 BusyGrace 倍。
func TestBusyGraceExtendsTimeout(t *testing.T) {
	withTimeoutConfig(t, 60*time.Second, 5*time.Second)

	now := time.Now()
	// observedInterval 5s → 阈值 60s，忙期宽限 = 180s
	s := &Session{
		LastSeen:         now.Add(-150 * time.Second),
		BusyUntil:        now.Add(30 * time.Second),
		observedInterval: 5 * time.Second,
	}
	if !s.isAliveAt(now) {
		t.Fatal("忙期 150s 无心跳仍应判存活（60s×3=180s 宽限）")
	}
	s.LastSeen = now.Add(-200 * time.Second)
	if s.isAliveAt(now) {
		t.Fatal("忙期超过宽限也应判离线")
	}
	// 忙期结束后回到正常阈值
	s.BusyUntil = now.Add(-time.Second)
	s.LastSeen = now.Add(-90 * time.Second)
	if s.isAliveAt(now) {
		t.Fatal("非忙期 90s 无心跳（>60s 阈值）应判离线")
	}
}

// ObserveHeartbeat：间隔变大立即生效，变小缓慢回收。
func TestObserveHeartbeatAdapts(t *testing.T) {
	s := &Session{LastSeen: time.Now()}
	s.ObserveHeartbeat() // 首次采样只记快照
	if s.observedInterval != 0 {
		t.Fatalf("首次采样不应产生间隔: %v", s.observedInterval)
	}

	s.LastSeen = s.LastSeen.Add(62 * time.Second)
	s.ObserveHeartbeat()
	if s.observedInterval != 62*time.Second {
		t.Fatalf("实测间隔 = %v, want 62s", s.observedInterval)
	}

	// 间隔变大：立即采用
	s.LastSeen = s.LastSeen.Add(120 * time.Second)
	s.ObserveHeartbeat()
	if s.observedInterval != 120*time.Second {
		t.Fatalf("间隔变大应立即采用: %v", s.observedInterval)
	}

	// 间隔变小：缓慢回收（不瞬间掉到太小）
	s.LastSeen = s.LastSeen.Add(5 * time.Second)
	s.ObserveHeartbeat()
	if s.observedInterval >= 120*time.Second || s.observedInterval <= 5*time.Second {
		t.Fatalf("间隔变小应平滑回收: %v", s.observedInterval)
	}

	// 多次采样后逐步收敛到快心跳附近
	for i := 0; i < 200; i++ {
		s.LastSeen = s.LastSeen.Add(5 * time.Second)
		s.ObserveHeartbeat()
	}
	if s.observedInterval > 15*time.Second {
		t.Fatalf("长期快心跳应收敛: %v", s.observedInterval)
	}
}

// LastSeen 未推进时不应改动采样值（例如 checker 高频调用）。
func TestObserveHeartbeatIgnoresUnchangedLastSeen(t *testing.T) {
	s := &Session{LastSeen: time.Now()}
	s.ObserveHeartbeat()
	s.observedInterval = 60 * time.Second
	for i := 0; i < 5; i++ {
		s.ObserveHeartbeat()
	}
	if s.observedInterval != 60*time.Second {
		t.Fatalf("心跳未推进不应改变采样: %v", s.observedInterval)
	}
}

// 回归（2026-09-24 实测事故）：下发任务不能把"事实上已失联"的会话复活成在线。
//
// 现场：植入体 15:44:17 后消失；操作员在 15:45:35 / 15:46:13 / 15:47:37 各点了一次
// 命令，三次下发全部失败（no writer），但 Create() 每次仍无条件 MarkSessionBusy(0)
// → 忙期 = 2×180s = 6min；忙期内判活放宽 BusyGrace 倍，界面因此一直显示"在线"，
// 直到 15:53:41 忙期自然过期才翻成离线 —— 期间每条命令、每次 Shell 都发不出去。
func TestMarkBusyCannotResurrectDeadSession(t *testing.T) {
	withTimeoutConfig(t, 180*time.Second, 60*time.Second)

	now := time.Now()
	// 已静默 300s：远超正常窗口（observedInterval 60s → 阈值 180s）
	s := &Session{LastSeen: now.Add(-300 * time.Second), observedInterval: 60 * time.Second}
	if s.isAliveAt(now) {
		t.Fatal("前置条件错误：静默 300s 应已判离线")
	}

	s.MarkBusy(0) // 模拟 Create() 的无条件标记

	if s.isAliveAt(now) {
		t.Fatal("给已失联会话标记忙期后仍被判存活 —— 界面会一直显示在线，但命令发不出去")
	}
	if !s.BusyUntil.IsZero() {
		t.Fatalf("失联会话不应被写入忙期，BusyUntil=%v", s.BusyUntil)
	}
}

// 忙期"延长活着的会话"这条语义必须保留（长任务期间心跳可能停顿）。
func TestMarkBusyStillExtendsLiveSession(t *testing.T) {
	withTimeoutConfig(t, 180*time.Second, 60*time.Second)

	now := time.Now()
	s := &Session{LastSeen: now, observedInterval: 60 * time.Second}
	s.MarkBusy(0) // 标记时存活 → 忙期 = 2×180s = 360s

	s.LastSeen = now.Add(-300 * time.Second) // 已过正常窗口 180s
	if !s.isAliveAt(now) {
		t.Fatal("存活时标记的忙期应继续放宽判活窗口（180s×3=540s 内）")
	}
}

// 下发失败必须清掉忙期，否则每点一次命令就续一次"假在线"。
func TestClearSessionBusyRestoresNormalWindow(t *testing.T) {
	withTimeoutConfig(t, 180*time.Second, 60*time.Second)

	now := time.Now()
	m := &Manager{sessions: make(map[string]*Session)}
	sess := &Session{
		Info:             &types.SessionInfo{ID: "s1", Status: "active", LastSeen: now},
		LastSeen:         now,
		observedInterval: 60 * time.Second,
	}
	m.sessions["s1"] = sess

	m.MarkSessionBusy("s1", 0)
	sess.LastSeen = now.Add(-300 * time.Second)
	if !sess.isAliveAt(now) {
		t.Fatal("忙期应放宽判活窗口")
	}

	m.ClearSessionBusy("s1") // 模拟下发失败时的清理
	if sess.isAliveAt(now) {
		t.Fatal("清忙期后应回到正常窗口（300s > 180s → 判离线）")
	}
}
