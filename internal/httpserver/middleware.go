package httpserver

import (
	"net/http"
	"runtime/debug"
	"time"

	"github.com/hequan2017/bigmodel/internal/api"
)

// statusRecorder 记录响应状态码，供访问日志与 panic 恢复使用。
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// wrap 组装中间件链：请求 ID → panic 恢复 → 访问日志。
func (s *Server) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" {
			reqID = api.NewRequestID()
		}
		w.Header().Set("X-Request-ID", reqID)
		r = r.WithContext(api.WithRequestID(r.Context(), reqID))

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		defer func() {
			if p := recover(); p != nil {
				s.log.Error("panic 已恢复",
					"request_id", reqID,
					"panic", p,
					"stack", string(debug.Stack()),
				)
				api.WriteError(rec, r, http.StatusInternalServerError, "internal_error", "服务内部错误，请稍后重试")
			}
			s.log.Info("http_request",
				"request_id", reqID,
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"remote_addr", r.RemoteAddr,
			)
		}()

		next.ServeHTTP(rec, r)
	})
}
