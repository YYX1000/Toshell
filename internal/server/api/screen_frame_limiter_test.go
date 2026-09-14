package api

import (
	"testing"
	"time"
)

func TestScreenFrameLimiterThrottlesByFPS(t *testing.T) {
	l := newScreenFrameLimiter()
	l.SetFPS("sess", 2) // 500ms/帧

	if !l.Allow("sess") {
		t.Fatal("首帧应放行")
	}
	if l.Allow("sess") {
		t.Fatal("同一间隔内的第二帧应被丢弃（帧合并）")
	}
	if got := l.Dropped("sess"); got != 1 {
		t.Fatalf("丢弃计数 = %d, want 1", got)
	}

	time.Sleep(520 * time.Millisecond)
	if !l.Allow("sess") {
		t.Fatal("超过间隔后应放行")
	}
	if l.Dropped("sess") != 1 {
		t.Fatalf("放行不应增加丢弃计数: %d", l.Dropped("sess"))
	}
}

// 硬上限 10fps：即使请求 30fps 也不会把 WS 打爆。
func TestScreenFrameLimiterHardFloor(t *testing.T) {
	l := newScreenFrameLimiter()
	l.SetFPS("fast", 30)

	if !l.Allow("fast") {
		t.Fatal("首帧应放行")
	}
	time.Sleep(40 * time.Millisecond)
	if l.Allow("fast") {
		t.Fatal("40ms < 100ms 硬下限，应丢弃")
	}
	time.Sleep(80 * time.Millisecond)
	if !l.Allow("fast") {
		t.Fatal("累计 >100ms 后应放行")
	}
}

// 未声明帧率的会话按 2fps 兜底。
func TestScreenFrameLimiterDefaultFPS(t *testing.T) {
	l := newScreenFrameLimiter()
	if !l.Allow("unknown") {
		t.Fatal("首帧应放行")
	}
	if l.Allow("unknown") {
		t.Fatal("兜底 2fps 下应立即丢弃第二帧")
	}
}

func TestScreenFrameLimiterReset(t *testing.T) {
	l := newScreenFrameLimiter()
	l.SetFPS("s", 1)
	l.Allow("s")
	l.Allow("s")
	if l.Dropped("s") == 0 {
		t.Fatal("应有丢弃计数")
	}
	l.Reset("s")
	if l.Dropped("s") != 0 {
		t.Fatal("Reset 应清空计数")
	}
	if !l.Allow("s") {
		t.Fatal("Reset 后首帧应放行")
	}
}

func TestIntParamClamping(t *testing.T) {
	five, zero, huge, neg := 5, 0, 99999, -3
	cases := []struct {
		name string
		v    *int
		def  int
		min  int
		max  int
		want int
	}{
		{"nil用默认", nil, 2, 1, 10, 2},
		{"正常值", &five, 2, 1, 10, 5},
		{"低于下限", &neg, 0, 0, 16, 0},
		{"高于上限", &huge, 0, 0, 16, 16},
		{"零值合法", &zero, 70, 0, 100, 0},
	}
	for _, c := range cases {
		if got := intParam(c.v, c.def, c.min, c.max); got != c.want {
			t.Errorf("%s: intParam = %d, want %d", c.name, got, c.want)
		}
	}
}
