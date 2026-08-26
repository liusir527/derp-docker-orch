package api

import (
	"embed"
	"net/http"
)

// webFS 内嵌双页面资源,无构建步骤。
//
//go:embed web/*.html
var webFS embed.FS

// handleRedeemPage 用户兑换面板(公开)。
func (s *Server) handleRedeemPage(w http.ResponseWriter, r *http.Request) {
	data, err := webFS.ReadFile("web/redeem.html")
	if err != nil {
		http.Error(w, "页面资源缺失", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}

// handleAdminPage 管理面板(页面公开,数据接口 session 认证)。
func (s *Server) handleAdminPage(w http.ResponseWriter, r *http.Request) {
	data, err := webFS.ReadFile("web/admin.html")
	if err != nil {
		http.Error(w, "页面资源缺失", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}
