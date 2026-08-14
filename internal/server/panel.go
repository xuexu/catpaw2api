package server

import (
	_ "embed"
	"net/http"
	"strings"
)

//go:embed panel.html
var panelHTMLRaw string

func (h *Handler) servePanel(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/admin" && r.URL.Path != "/panel" && r.URL.Path != "/panel/" {
		http.NotFound(w, r)
		return
	}
	html := panelHTMLRaw
	html = strings.ReplaceAll(html, "__SERVICE_NAME__", "catpaw2api")
	html = strings.ReplaceAll(html, "__SERVICE_TITLE__", "CatPaw2API")
	html = strings.ReplaceAll(html, "__LOGO__", "CP")
	html = strings.ReplaceAll(html, "__ACCENT__", "#ea580c")
	// CatPaw：无 token 保活；「签到」按钮文案改为申请额度
	html = strings.ReplaceAll(html, ">全员签到<", ">申请额度<")
	html = strings.ReplaceAll(html, ">保活 Token<", ">刷新余额<")
	html = strings.ReplaceAll(html, ">签到<", ">申请<")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(html))
}
