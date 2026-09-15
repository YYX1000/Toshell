package api

import (
	"encoding/json"
	"net/http"

	"github.com/gorilla/mux"

	"toshell/internal/server/drivers"
)

// listDriversHandler 返回**操作员自备**的 BYOVD 驱动目录（服务端不再内置任何驱动）：
// 扫描 exe 同目录 drivers/、CWD drivers/、data/drivers/ 下的 *.sys + manifest.json。
// 前端据此展示"可加载的驱动"；列表为空表示需要自行放置/上传 .sys。
func (s *Server) listDriversHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	list := drivers.List()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"drivers":       list,
		"count":         len(list),
		"search_dirs":   drivers.SearchDirs(),
		"builtin":       false,
		"manifest_hint": "把 .sys 放进任一 search_dirs，并在同目录 manifest.json 里声明 device/service/ioctl（或在前端加载时手填）",
	})
}

// downloadDriverHandler 返回内置驱动原始二进制（仅允许目录内名称，防路径穿越）。
func (s *Server) downloadDriverHandler(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	d, data, err := drivers.Get(name)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+d.Name+`"`)
	w.Header().Set("X-Driver-Name", d.Name)
	w.Header().Set("X-Driver-Device", d.Device)
	w.Header().Set("X-Driver-Service", d.Service)
	w.Header().Set("X-Driver-SHA256", d.SHA256)
	w.Write(data)
}
