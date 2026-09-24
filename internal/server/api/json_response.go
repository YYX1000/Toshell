package api

import (
	"encoding/json"
	"net/http"
)

// writeJSONError 以**合法 JSON** 返回错误。
//
// 为什么需要它：原来很多地方写的是
//
//	http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), 500)
//
// 只要 err 里含双引号或换行（构建/编译失败信息几乎必然含换行，例如
// "DLL 构建失败（c-shared + mingw）：exit status 1\n输出：..."），产出的就不是合法
// JSON —— 前端 JSON.parse 直接抛错，用户只看到"解析失败"而不是真正的失败原因
// （v1.3.5 实测踩到：Node 侧 fetch 后 JSON.parse 报 Bad control character）。
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
