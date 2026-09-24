package builder

import (
	"encoding/binary"
	"strings"
	"testing"
)

// 本测试**不依赖任何外部文件**：在内存里手工拼最小 PE 镜像来覆盖各条预检分支
// （合规 amd64、i386 对 amd64 宿主、Go 载荷、.NET/CLR、TLS、无重定位、坏数据）。
//
// 说明：仓库所在机器有安全软件会删除/拦截新生成的 PE，因此这里只构造字节、
// 不落盘、不执行任何二进制。

// testPESpec 描述一个待构造的最小 PE。
type testPESpec struct {
	machine   uint16
	is64      bool
	isDLL     bool
	entryRVA  uint32
	subsystem uint16
	sections  []testPESection
	importRVA uint32
	relocRVA  uint32
	tlsRVA    uint32
	clrRVA    uint32
	tail      []byte   // 追加到文件末尾（用于塞 Go 特征字符串等）
	coffStrs  []string // 非空时在文件末尾写 COFF 字符串表，用于测 "/nnn" 长节名解析
}

type testPESection struct {
	name       string
	rva        uint32
	vsize      uint32
	rawSize    uint32
	chars      uint32
	data       []byte
	forceRawOf bool // 覆盖 RawSize（测试用非默认值）
}

func testPEAlign(v, a int) int {
	if a <= 0 {
		return v
	}
	return (v + a - 1) / a * a
}

// buildTestPE 拼一个头部合法、节表可解析的最小 PE（PE32 或 PE32+）。
func buildTestPE(spec testPESpec) []byte {
	optSize := 224
	dataDirOff := 96
	if spec.is64 {
		optSize = 240
		dataDirOff = 112
	}
	eLfanew := 0x80
	secTableOff := eLfanew + 4 + 20 + optSize
	rawOff := testPEAlign(secTableOff+len(spec.sections)*peSectionHeaderSize, 0x200)

	need := rawOff
	for _, s := range spec.sections {
		need += testPEAlign(len(s.data), 0x200)
	}
	// 可选的 COFF 字符串表：4 字节长度 + 以 NUL 结尾的字符串，紧跟在节数据之后。
	var coffTable []byte
	if len(spec.coffStrs) > 0 {
		var body []byte
		for _, s := range spec.coffStrs {
			body = append(body, []byte(s)...)
			body = append(body, 0)
		}
		coffTable = make([]byte, 4+len(body))
		binary.LittleEndian.PutUint32(coffTable[0:4], uint32(4+len(body)))
		copy(coffTable[4:], body)
	}
	buf := make([]byte, need+len(spec.tail)+len(coffTable))

	// DOS 头
	binary.LittleEndian.PutUint16(buf[0:2], peDOSMagic)
	binary.LittleEndian.PutUint32(buf[0x3C:0x40], uint32(eLfanew))

	// PE 签名 + IMAGE_FILE_HEADER
	binary.LittleEndian.PutUint32(buf[eLfanew:eLfanew+4], peNTSignature)
	fileHdr := eLfanew + 4
	binary.LittleEndian.PutUint16(buf[fileHdr:fileHdr+2], spec.machine)
	binary.LittleEndian.PutUint16(buf[fileHdr+2:fileHdr+4], uint16(len(spec.sections)))
	binary.LittleEndian.PutUint16(buf[fileHdr+16:fileHdr+18], uint16(optSize))
	if spec.isDLL {
		binary.LittleEndian.PutUint16(buf[fileHdr+18:fileHdr+20], imageFileDLL)
	}

	// 可选头
	optHdr := fileHdr + 20
	magic := uint16(peMagic32)
	if spec.is64 {
		magic = peMagic64
	}
	binary.LittleEndian.PutUint16(buf[optHdr:optHdr+2], magic)
	binary.LittleEndian.PutUint32(buf[optHdr+16:optHdr+20], spec.entryRVA)
	binary.LittleEndian.PutUint32(buf[optHdr+56:optHdr+60], 0x4000)         // SizeOfImage
	binary.LittleEndian.PutUint32(buf[optHdr+60:optHdr+64], uint32(rawOff)) // SizeOfHeaders
	binary.LittleEndian.PutUint16(buf[optHdr+68:optHdr+70], spec.subsystem) // Subsystem
	binary.LittleEndian.PutUint32(buf[optHdr+dataDirOff-4:], peMaxDataDirs) // NumberOfRvaAndSizes

	putDir := func(idx int, rva uint32) {
		off := optHdr + dataDirOff + idx*8
		binary.LittleEndian.PutUint32(buf[off:off+4], rva)
		if rva != 0 {
			binary.LittleEndian.PutUint32(buf[off+4:off+8], 0x10) // 目录大小（非 0，更贴近真实镜像）
		}
	}
	putDir(peDirImport, spec.importRVA)
	putDir(peDirReloc, spec.relocRVA)
	putDir(peDirTLS, spec.tlsRVA)
	putDir(peDirCLR, spec.clrRVA)

	// 节表 + 节数据
	cur := rawOff
	for i, s := range spec.sections {
		off := secTableOff + i*peSectionHeaderSize
		copy(buf[off:off+peSectionNameLen], s.name)
		binary.LittleEndian.PutUint32(buf[off+8:off+12], s.vsize)
		binary.LittleEndian.PutUint32(buf[off+12:off+16], s.rva)
		rawSize := uint32(testPEAlign(len(s.data), 0x200))
		if s.forceRawOf {
			rawSize = s.rawSize
		}
		binary.LittleEndian.PutUint32(buf[off+16:off+20], rawSize)
		binary.LittleEndian.PutUint32(buf[off+20:off+24], uint32(cur))
		binary.LittleEndian.PutUint32(buf[off+36:off+40], s.chars)
		copy(buf[cur:], s.data)
		cur += testPEAlign(len(s.data), 0x200)
	}
	copy(buf[cur:], spec.tail)
	if len(coffTable) > 0 {
		// PointerToSymbolTable 指向字符串表首（NumberOfSymbols=0，测试里不需要真符号表）。
		off := cur + len(spec.tail)
		copy(buf[off:], coffTable)
		binary.LittleEndian.PutUint32(buf[fileHdr+8:fileHdr+12], uint32(off))
	}
	return buf
}

const (
	testScnCode = 0x60000020 // .text：可执行 + 可读 + 含代码
	testScnData = 0xC0000040 // .data：可读写 + 已初始化数据
	testScnRsrc = 0x40000040 // .rsrc/.reloc：只读 + 已初始化数据
)

// amd64EXE 合规的 amd64 控制台 EXE：带重定位表、无 TLS、无 CLR、非 Go。
func amd64EXE() []byte {
	return buildTestPE(testPESpec{
		machine:   imageFileMachineAMD64,
		is64:      true,
		entryRVA:  0x1000,
		subsystem: peSubsystemWindowsConsole,
		importRVA: 0x2000,
		relocRVA:  0x3000,
		sections: []testPESection{
			{name: ".text", rva: 0x1000, vsize: 0x200, chars: testScnCode, data: []byte{0x90, 0xC3}},
			{name: ".rdata", rva: 0x2000, vsize: 0x200, chars: testScnRsrc},
			{name: ".data", rva: 0x3000, vsize: 0x200, chars: testScnData},
		},
	})
}

// TestInspectPE_AMD64EXE 合法 amd64 PE → 预检 ok，字段解析正确。
func TestInspectPE_AMD64EXE(t *testing.T) {
	data := amd64EXE()
	info, err := InspectPE(data)
	if err != nil {
		t.Fatalf("InspectPE 解析自造 PE 失败: %v", err)
	}
	if info.Machine != "amd64" {
		t.Errorf("Machine = %q, want amd64", info.Machine)
	}
	if info.MachineRaw != imageFileMachineAMD64 {
		t.Errorf("MachineRaw = 0x%04X, want 0x%04X", info.MachineRaw, imageFileMachineAMD64)
	}
	if !info.Is64Bit {
		t.Error("PE32+ 载荷 Is64Bit 应为 true")
	}
	if info.IsDLL {
		t.Error("普通 EXE 的 IsDLL 应为 false")
	}
	if info.HasTLSDir || info.HasCLRDir {
		t.Errorf("不该有 TLS/CLR 目录: tls=%v clr=%v", info.HasTLSDir, info.HasCLRDir)
	}
	if !info.HasRelocDir {
		t.Error("数据目录 index 5 非 0，HasRelocDir 应为 true")
	}
	if !info.HasImportDir {
		t.Error("数据目录 index 1 非 0，HasImportDir 应为 true")
	}
	if info.Subsystem != peSubsystemWindowsConsole {
		t.Errorf("Subsystem = %d, want %d", info.Subsystem, peSubsystemWindowsConsole)
	}
	if info.EntryPointRVA != 0x1000 {
		t.Errorf("EntryPointRVA = 0x%X, want 0x1000", info.EntryPointRVA)
	}
	if info.ImageSize != 0x4000 {
		t.Errorf("ImageSize = 0x%X, want 0x4000", info.ImageSize)
	}
	if info.SizeOfHeaders == 0 {
		t.Error("SizeOfHeaders 不应为 0")
	}
	if len(info.Sections) != 3 {
		t.Fatalf("解析到 %d 个节, want 3", len(info.Sections))
	}
	if info.Sections[0].Name != ".text" || !info.Sections[0].Executable() {
		t.Errorf(".text 应可执行: %+v", info.Sections[0])
	}
	if info.Sections[2].Name != ".data" || info.Sections[2].Executable() {
		t.Errorf(".data 不该可执行: %+v", info.Sections[2])
	}
	if info.Sections[0].VSize != 0x200 || info.Sections[0].RVA != 0x1000 {
		t.Errorf(".text VSize/RVA 解析错误: %+v", info.Sections[0])
	}
	if !info.ConsoleApp() {
		t.Error("Subsystem=3 应识别为控制台程序")
	}

	verdict, reasons, _ := CheckMemoryExec(info, false, "amd64", "exe_mem")
	if verdict != VerdictOK {
		t.Errorf("合规 amd64 EXE 在 amd64 宿主上应 ok, got %s, reasons=%v", verdict, reasons)
	}
	if len(reasons) != 0 {
		t.Errorf("ok 时不该有理由: %v", reasons)
	}
}

// TestInspectPE_I386OnAMD64 架构不匹配：exe_mem 必须 reject，exe(donut) 只 warn。
func TestInspectPE_I386OnAMD64(t *testing.T) {
	data := buildTestPE(testPESpec{
		machine:   imageFileMachineI386,
		entryRVA:  0x1000,
		subsystem: peSubsystemWindowsConsole,
		relocRVA:  0x3000,
		sections: []testPESection{
			{name: ".text", rva: 0x1000, vsize: 0x200, chars: testScnCode},
			{name: ".reloc", rva: 0x3000, vsize: 0x200, chars: testScnRsrc},
		},
	})
	info, err := InspectPE(data)
	if err != nil {
		t.Fatalf("InspectPE 失败: %v", err)
	}
	if info.Machine != "386" || info.Is64Bit {
		t.Fatalf("i386 PE 应归一为 386 且非 64 位: machine=%q is64=%v", info.Machine, info.Is64Bit)
	}

	verdict, reasons, suggestion := CheckMemoryExec(info, false, "amd64", "exe_mem")
	if verdict != VerdictReject {
		t.Fatalf("32 位载荷进 64 位宿主 exe_mem 应 reject, got %s", verdict)
	}
	if len(reasons) == 0 || !strings.Contains(reasons[0], "架构不匹配") {
		t.Errorf("reject 理由应说明架构不匹配, got %v", reasons)
	}
	if !strings.Contains(suggestion, "donut") {
		t.Errorf("建议应提到 donut 转换, got %q", suggestion)
	}

	verdictExe, reasonsExe, _ := CheckMemoryExec(info, false, "amd64", "exe")
	if verdictExe != VerdictWarn {
		t.Fatalf("exe(donut) 路径架构不匹配应 warn, got %s (reasons=%v)", verdictExe, reasonsExe)
	}

	// 宿主架构未知（空串）时跳过架构检查，其余条件都合规 → ok。
	verdictUnknown, _, _ := CheckMemoryExec(info, false, "", "exe_mem")
	if verdictUnknown != VerdictOK {
		t.Errorf("宿主架构未知应跳过架构检查, got %s", verdictUnknown)
	}
}

// TestDetectGoBinary 节名 / 构建魔数 / 构建标识 / 运行时符号四条识别路径 + garble 改名后的兜底。
func TestDetectGoBinary(t *testing.T) {
	// 1a) 带 gopclntab 节。注意 PE 的节名字段只有 8 字节，".gopclntab" 会被截断成
	// ".gopclnt"，所以识别用 gopcln / pclntab 前缀匹配。
	goPE := buildTestPE(testPESpec{
		machine:   imageFileMachineAMD64,
		is64:      true,
		entryRVA:  0x1000,
		subsystem: peSubsystemWindowsConsole,
		relocRVA:  0x3000,
		sections: []testPESection{
			{name: ".text", rva: 0x1000, vsize: 0x200, chars: testScnCode, data: []byte("runtime.main")},
			{name: ".gopclntab", rva: 0x2000, vsize: 0x200, chars: testScnRsrc, data: []byte("pclntab")},
			{name: ".data", rva: 0x3000, vsize: 0x200, chars: testScnData},
		},
	})
	if info := InspectPEMust(t, goPE); len(info.Sections) == 3 && info.Sections[1].Name != ".gopclnt" {
		t.Errorf("节名字段 8 字节应截断为 .gopclnt, got %q", info.Sections[1].Name)
	}
	isGo, evidence := DetectGoBinary(goPE)
	if !isGo || !strings.Contains(evidence, "gopcln") {
		t.Fatalf("带 gopclntab 节应识别为 Go: isGo=%v evidence=%q", isGo, evidence)
	}

	// 1b) "/nnn" 长节名要从 COFF 字符串表还原成 .gopclntab（真实 Go PE 的 .zdebug_* 就是这种形式）。
	longPE := buildTestPE(testPESpec{
		machine:   imageFileMachineAMD64,
		is64:      true,
		entryRVA:  0x1000,
		subsystem: peSubsystemWindowsConsole,
		sections: []testPESection{
			{name: "/4", rva: 0x2000, vsize: 0x200, chars: testScnRsrc},
		},
		coffStrs: []string{".gopclntab"},
	})
	info := InspectPEMust(t, longPE)
	if len(info.Sections) != 1 || info.Sections[0].Name != ".gopclntab" {
		t.Fatalf("/4 长节名应从字符串表还原, got %+v", info.Sections)
	}
	if isGo, evidence := DetectGoBinary(longPE); !isGo || !strings.Contains(evidence, ".gopclntab") {
		t.Errorf("长节名 .gopclntab 应识别为 Go: isGo=%v evidence=%q", isGo, evidence)
	}

	info, err := InspectPE(goPE)
	if err != nil {
		t.Fatalf("InspectPE 失败: %v", err)
	}
	verdict, reasons, suggestion := CheckMemoryExec(info, isGo, "amd64", "exe_mem")
	if verdict != VerdictReject {
		t.Fatalf("Go 载荷走 exe_mem 应 reject, got %s", verdict)
	}
	if len(reasons) == 0 || !strings.Contains(strings.Join(reasons, "\n"), "Go runtime") {
		t.Errorf("reject 理由应说明双 Go runtime 冲突, got %v", reasons)
	}
	if !strings.Contains(suggestion, "落地执行") {
		t.Errorf("建议应包含落地执行, got %q", suggestion)
	}
	// donut 路径不阻断，只 warn。
	if v, _, _ := CheckMemoryExec(info, isGo, "amd64", "exe"); v != VerdictWarn {
		t.Errorf("Go 载荷走 exe(donut) 应 warn, got %s", v)
	}

	// 2) garble 改名（节名不含 gopclntab）→ 构建信息魔数兜底。
	garblePE := buildTestPE(testPESpec{
		machine:   imageFileMachineAMD64,
		is64:      true,
		entryRVA:  0x1000,
		subsystem: peSubsystemWindowsConsole,
		relocRVA:  0x3000,
		sections: []testPESection{
			{name: ".text", rva: 0x1000, vsize: 0x200, chars: testScnCode},
		},
		tail: []byte(goBuildInfoMagic + "go1.25.0"),
	})
	if isGo, evidence := DetectGoBinary(garblePE); !isGo || !strings.Contains(evidence, "buildinf") {
		t.Errorf("构建信息魔数兜底失败: isGo=%v evidence=%q", isGo, evidence)
	}

	// 3) 只剩运行时符号名（极端改名）→ 仍能兜底。
	symPE := buildTestPE(testPESpec{
		machine:   imageFileMachineAMD64,
		is64:      true,
		entryRVA:  0x1000,
		subsystem: peSubsystemWindowsConsole,
		sections: []testPESection{
			{name: ".text", rva: 0x1000, vsize: 0x200, chars: testScnCode, data: []byte("_\x00runtime.goexit\x00")},
		},
	})
	if isGo, evidence := DetectGoBinary(symPE); !isGo || !strings.Contains(evidence, "runtime.goexit") {
		t.Errorf("运行时符号兜底失败: isGo=%v evidence=%q", isGo, evidence)
	}

	// 4) Go 写进 PE 头的构建标识字符串（实测 32 位 Go PE 在 0x472 处就有 "Go build ID:"）。
	bidPE := buildTestPE(testPESpec{
		machine:   imageFileMachineAMD64,
		is64:      true,
		entryRVA:  0x1000,
		subsystem: peSubsystemWindowsConsole,
		sections: []testPESection{
			{name: ".text", rva: 0x1000, vsize: 0x200, chars: testScnCode},
		},
		tail: []byte("Go build ID: \"abcdef\""),
	})
	if isGo, evidence := DetectGoBinary(bidPE); !isGo || !strings.Contains(evidence, "build ID") {
		t.Errorf("Go build ID 兜底失败: isGo=%v evidence=%q", isGo, evidence)
	}

	// 5) 普通 C/汇编 PE 不应被误判。
	if isGo, evidence := DetectGoBinary(amd64EXE()); isGo {
		t.Errorf("普通 PE 被误判为 Go: %q", evidence)
	}
	if isGo, evidence := DetectGoBinary(nil); isGo || evidence != "" {
		t.Errorf("空数据应返回 false/空依据, got %v %q", isGo, evidence)
	}
}

// TestInspectPE_CLR .NET 程序集：exe_mem 必须 reject。
func TestInspectPE_CLR(t *testing.T) {
	data := buildTestPE(testPESpec{
		machine:   imageFileMachineAMD64,
		is64:      true,
		entryRVA:  0x1000,
		subsystem: peSubsystemWindowsConsole,
		importRVA: 0x2000,
		relocRVA:  0x3000,
		clrRVA:    0x4000,
		sections: []testPESection{
			{name: ".text", rva: 0x1000, vsize: 0x200, chars: testScnCode},
			{name: ".reloc", rva: 0x3000, vsize: 0x200, chars: testScnRsrc},
		},
	})
	info, err := InspectPE(data)
	if err != nil {
		t.Fatalf("InspectPE 失败: %v", err)
	}
	if !info.HasCLRDir {
		t.Fatal("数据目录 index 14 非 0，HasCLRDir 应为 true")
	}
	verdict, reasons, suggestion := CheckMemoryExec(info, false, "amd64", "exe_mem")
	if verdict != VerdictReject {
		t.Fatalf(".NET 载荷走 exe_mem 应 reject, got %s", verdict)
	}
	if !strings.Contains(strings.Join(reasons, "\n"), ".NET") {
		t.Errorf("理由应说明是 .NET 程序集, got %v", reasons)
	}
	if !strings.Contains(suggestion, "落地执行") || !strings.Contains(suggestion, "donut") {
		t.Errorf("建议应给出落地执行 / donut 两条路, got %q", suggestion)
	}
}

// TestCheckMemoryExec_TLSAndNoReloc TLS 与缺重定位表只 warn，不阻断。
func TestCheckMemoryExec_TLSAndNoReloc(t *testing.T) {
	tlsPE := buildTestPE(testPESpec{
		machine:   imageFileMachineAMD64,
		is64:      true,
		entryRVA:  0x1000,
		subsystem: peSubsystemWindowsConsole,
		relocRVA:  0x3000,
		tlsRVA:    0x4000,
		sections: []testPESection{
			{name: ".text", rva: 0x1000, vsize: 0x200, chars: testScnCode},
			{name: ".reloc", rva: 0x3000, vsize: 0x200, chars: testScnRsrc},
		},
	})
	info, err := InspectPE(tlsPE)
	if err != nil {
		t.Fatalf("InspectPE 失败: %v", err)
	}
	if !info.HasTLSDir {
		t.Fatal("HasTLSDir 应为 true")
	}
	verdict, reasons, _ := CheckMemoryExec(info, false, "amd64", "exe_mem")
	if verdict != VerdictWarn {
		t.Fatalf("带 TLS 回调应 warn, got %s", verdict)
	}
	if !strings.Contains(strings.Join(reasons, "\n"), "TLS") {
		t.Errorf("warn 理由应提到 TLS, got %v", reasons)
	}

	// 无重定位表且非 DLL → warn。
	noReloc := buildTestPE(testPESpec{
		machine:   imageFileMachineAMD64,
		is64:      true,
		entryRVA:  0x1000,
		subsystem: peSubsystemWindowsConsole,
		importRVA: 0x2000,
		sections: []testPESection{
			{name: ".text", rva: 0x1000, vsize: 0x200, chars: testScnCode},
		},
	})
	info2, err := InspectPE(noReloc)
	if err != nil {
		t.Fatalf("InspectPE 失败: %v", err)
	}
	if info2.HasRelocDir {
		t.Fatal("没有 .reloc 时 HasRelocDir 应为 false")
	}
	verdict2, reasons2, suggestion2 := CheckMemoryExec(info2, false, "amd64", "exe_mem")
	if verdict2 != VerdictWarn {
		t.Fatalf("无重定位表应 warn, got %s", verdict2)
	}
	if !strings.Contains(strings.Join(reasons2, "\n"), "重定位") {
		t.Errorf("warn 理由应提到重定位, got %v", reasons2)
	}
	if !strings.Contains(suggestion2, "reloc") {
		t.Errorf("建议应提到 .reloc, got %q", suggestion2)
	}
}

// TestCheckMemoryExec_DLLFlags DLL 标志与 kind 不符时给出提示。
func TestCheckMemoryExec_DLLFlags(t *testing.T) {
	dll := buildTestPE(testPESpec{
		machine:   imageFileMachineAMD64,
		is64:      true,
		isDLL:     true,
		entryRVA:  0x1000,
		subsystem: peSubsystemWindowsConsole,
		importRVA: 0x2000,
		relocRVA:  0x3000,
		sections: []testPESection{
			{name: ".text", rva: 0x1000, vsize: 0x200, chars: testScnCode},
			{name: ".reloc", rva: 0x3000, vsize: 0x200, chars: testScnRsrc},
		},
	})
	info, err := InspectPE(dll)
	if err != nil {
		t.Fatalf("InspectPE 失败: %v", err)
	}
	if !info.IsDLL {
		t.Fatal("IMAGE_FILE_DLL 已置位，IsDLL 应为 true")
	}
	// 合规 DLL 走 kind=dll → ok。
	if v, reasons, _ := CheckMemoryExec(info, false, "amd64", "dll"); v != VerdictOK {
		t.Errorf("合规 DLL 走 dll 应 ok, got %s (reasons=%v)", v, reasons)
	}
	// DLL 走 exe_mem → warn（入口点语义不对）。
	if v, _, _ := CheckMemoryExec(info, false, "amd64", "exe_mem"); v != VerdictWarn {
		t.Errorf("DLL 走 exe_mem 应 warn, got %s", v)
	}
	// EXE 走 dll → warn。
	if v, _, _ := CheckMemoryExec(InspectPEMust(t, amd64EXE()), false, "amd64", "dll"); v != VerdictWarn {
		t.Errorf("EXE 走 dll 应 warn, got %s", v)
	}
}

// TestInspectPE_BadData 坏数据必须返回 error（而不是 panic 或"看起来 ok"）。
func TestInspectPE_BadData(t *testing.T) {
	valid := amd64EXE()
	badMZ := append([]byte(nil), valid...)
	badMZ[0] = 'X'

	badSig := append([]byte(nil), valid...)
	badSig[0x80] = 'X'

	truncated := valid[:0x100]

	// 可选头魔数损坏
	badMagic := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint16(badMagic[0x80+4+20:], 0x1234)

	// 节表被截断：文件头声明有节，但文件在节表前结束
	noSections := append([]byte(nil), valid[:0x80+4+20+240]...)

	cases := []struct {
		name string
		data []byte
	}{
		{"空数据", nil},
		{"太短", []byte("MZ")},
		{"非 PE 文本", []byte(strings.Repeat("not a pe file. ", 20))},
		{"MZ 但 e_lfanew 越界", append([]byte("MZ"), make([]byte, 0x3C)...)},
		{"DOS 魔数损坏", badMZ},
		{"PE 签名损坏", badSig},
		{"可选头魔数非法", badMagic},
		{"文件在头中间截断", truncated},
		{"声明有节但节表缺失", noSections},
	}
	for _, c := range cases {
		if _, err := InspectPE(c.data); err == nil {
			t.Errorf("%s: 期望 InspectPE 返回 error，实际为 nil", c.name)
		}
	}

	// e_lfanew 越界的用例需要真的写入越界偏移。
	// 0x7FFFFFF0 专门覆盖 32 位构建下 "+24 溢出成负数绕过上界检查"的老坑。
	for _, lfanew := range []uint32{0x7FFFFFFF, 0x7FFFFFF0, 0xFFFFFFF0, 0x100000} {
		over := make([]byte, 0x40)
		binary.LittleEndian.PutUint16(over[0:2], peDOSMagic)
		binary.LittleEndian.PutUint32(over[0x3C:0x40], lfanew)
		if _, err := InspectPE(over); err == nil {
			t.Errorf("e_lfanew=0x%X 应返回 error", lfanew)
		}
	}

	// 声明 65535 个节但文件里放不下：允许"能读多少读多少"（打包/截断样本更宽容），
	// 但绝不能 panic、越界或凭空捏造出节来。
	hugeSec := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint16(hugeSec[0x80+4+2:], 0xFFFF)
	if info, err := InspectPE(hugeSec); err == nil && len(info.Sections) >= 0xFFFF {
		t.Errorf("节数超界时应截断解析, got %d 节", len(info.Sections))
	}
}

// TestPEInfoSummary 回传给前端的精简指纹字段齐全。
func TestPEInfoSummary(t *testing.T) {
	info := InspectPEMust(t, amd64EXE())
	sum := info.Summary(true)
	if sum.Machine != "amd64" || !sum.Is64Bit || sum.IsDLL || sum.HasTLS || sum.HasCLR || !sum.IsGo {
		t.Errorf("Summary 字段不符: %+v", sum)
	}
	if info.SubsystemName() == "" {
		t.Error("SubsystemName 不应为空")
	}
}

// InspectPEMust 测试辅助：解析失败直接 Fatal。
func InspectPEMust(t *testing.T, data []byte) *PEInfo {
	t.Helper()
	info, err := InspectPE(data)
	if err != nil {
		t.Fatalf("InspectPE 失败: %v", err)
	}
	return info
}
