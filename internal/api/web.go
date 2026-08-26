package api

import (
	"embed"
	"net/http"
)

// webFS 内嵌单页资源,无构建步骤。
//
//go:embed web/index.html
var webFS embed.FS

// handleWeb 返回单页管理界面。
func (s *Server) handleWeb(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "页面资源缺失", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}
