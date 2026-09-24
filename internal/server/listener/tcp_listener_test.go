package listener

import (
	"strings"
	"testing"
	"time"

	"toshell/internal/common/types"
	"toshell/internal/server/session"
)

// 回归（2026-09-24 实测事故）——端到端覆盖 PushTask 的失败分支：
// 任务下发失败必须清掉 task.Create() 设下的忙期。
//
// 现场：植入体 15:44:47 被对端 RST 后再没回来，操作员在 15:45:35 / 15:46:13 /
// 15:47:37 各点了一次命令，三次下发全部失败（no writer），但 Create() 每次仍
// 无条件 MarkSessionBusy(id, 0) → 忙期 = 2×180s = 6 分钟；忙期内判活窗口被放宽
// BusyGrace(3) 倍，于是界面一直显示"在线"，而每条命令、每次 Shell 都发不出去，
// 直到 15:53:41 忙期自然过期才翻成离线（= 15:47:37 + 360s）。
//
// 本用例不依赖真实植入体：构造一个"活着但没有任何 writer"的会话，
// 调 PushTask 必然失败，走的就是出问题的那条分支。
func TestPushTaskFailureClearsBusy(t *testing.T) {
	oldTimeout := session.HeartbeatTimeout
	session.HeartbeatTimeout = 180 * time.Second
	t.Cleanup(func() { session.HeartbeatTimeout = oldTimeout })

	mgr := session.New()
	const sid = "test-session-busy-clear"
	_ = mgr.Remove(sid)
	t.Cleanup(func() { _ = mgr.Remove(sid) })

	if err := mgr.Add(&types.SessionInfo{ID: sid, Status: "active", LastSeen: time.Now()}); err != nil {
		t.Fatalf("add session: %v", err)
	}
	sess, err := mgr.Get(sid)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}

	// 复刻 task.Manager.Create() 的行为：下发之前就无条件标忙
	mgr.MarkSessionBusy(sid, 0)
	if sess.BusyUntil.IsZero() {
		t.Fatal("前置条件不成立：刚心跳过的会话应被标上忙期")
	}

	// 该会话没有 writer → 下发必然失败，正是本次修补的分支
	l := &TCPListener{sessionMgr: mgr}
	err = l.PushTask(sid, &types.TaskInfo{ID: 1, TaskType: "command", Command: "whoami"})
	if err == nil || !strings.Contains(err.Error(), "no writer") {
		t.Fatalf("期望 no writer 失败，实际: %v", err)
	}

	if !sess.BusyUntil.IsZero() {
		t.Fatal("下发失败后忙期没被清掉 —— 失联会话会继续被显示成在线（本用例要防的正是这个）")
	}

	// 忙期清掉后必须回到正常判活窗口。
	// 注意阈值：该会话还没采样到心跳间隔，effectiveTimeout 会额外放宽到 2×基准
	// = 360s（首周期保护，见 TestEffectiveTimeoutHasHeartbeatMargin），
	// 忙期宽限则是它的 BusyGrace(3) 倍 = 1080s。
	// 取 500s：> 正常窗口 360s（修复后判离线），但 < 忙期宽限 1080s
	// （修复前会因残留忙期被判存活）—— 正好卡在有区分度的位置。
	sess.LastSeen = time.Now().Add(-500 * time.Second)
	if sess.IsAlive() {
		t.Fatal("忙期已清，500s 无心跳仍被判存活 —— 判活窗口没有回到正常值")
	}
}
