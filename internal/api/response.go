package api

import (
	"encoding/json"
	"net/http"
)

// WriteJSON 输出业务 JSON 响应。
func WriteJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// ErrorBody 是稳定错误码与用户可读说明。
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorEnvelope struct {
	Error     ErrorBody `json:"error"`
	RequestID string    `json:"request_id"`
}

// WriteError 输出统一错误响应，附带请求 ID 便于关联日志。
func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	WriteJSON(w, status, errorEnvelope{
		Error:     ErrorBody{Code: code, Message: message},
		RequestID: RequestIDFromContext(r.Context()),
	})
}
