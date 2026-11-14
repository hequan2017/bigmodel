package httpserver

import (
	"embed"
	"io/fs"
	"net/http"
)

// webFS 内嵌学生端页面（§15.1）：登录、考试列表/作答/倒计时/恢复/结果。
// 同源静态托管，不走跨域（§3.2）。
//
//go:embed web
var webFS embed.FS

func (s *Server) staticHandler() http.Handler {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "static assets unavailable", http.StatusInternalServerError)
		})
	}
	return http.FileServer(http.FS(sub))
}
