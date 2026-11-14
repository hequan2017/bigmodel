package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/hequan2017/bigmodel/internal/api"
	"github.com/hequan2017/bigmodel/internal/auth"
)

type loginRequest struct {
	LoginName string `json:"login_name"`
	Password  string `json:"password"`
}

// handleLogin 校验凭据并颁发会话令牌。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "请求体必须是 JSON")
		return
	}
	req.LoginName = strings.TrimSpace(req.LoginName)
	if req.LoginName == "" || req.Password == "" {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "login_name 与 password 不能为空")
		return
	}

	token, user, err := s.auth.Login(r.Context(), req.LoginName, req.Password)
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		api.WriteError(w, r, http.StatusUnauthorized, "invalid_credentials", "登录名或密码不正确")
		return
	case errors.Is(err, auth.ErrUserDisabled):
		api.WriteError(w, r, http.StatusUnauthorized, "user_disabled", "账号已停用")
		return
	case errors.Is(err, auth.ErrTooManyAttempts):
		api.WriteError(w, r, http.StatusTooManyRequests, "too_many_attempts", err.Error())
		return
	case err != nil:
		s.log.Error("登录处理失败", "error", err)
		api.WriteError(w, r, http.StatusInternalServerError, "internal_error", "登录失败，请稍后重试")
		return
	}

	api.WriteJSON(w, http.StatusOK, map[string]any{"token": token, "user": user})
}

// handleLogout 注销当前会话。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.Logout(r.Context(), auth.TokenFromContext(r.Context())); err != nil {
		s.log.Error("登出处理失败", "error", err)
		api.WriteError(w, r, http.StatusInternalServerError, "internal_error", "登出失败，请稍后重试")
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleMe 返回当前登录用户。
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, map[string]any{"user": auth.UserFromContext(r.Context())})
}

// bearerToken 解析 Authorization: Bearer <token>。
func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

// authMiddleware 校验会话令牌，并将用户与令牌注入 context。
func authMiddleware(authn auth.Authenticator, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		user, err := authn.Authenticate(r.Context(), token)
		if err != nil {
			api.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "未登录或会话已失效")
			return
		}
		ctx := auth.WithUser(r.Context(), user)
		ctx = auth.WithToken(ctx, token)
		next(w, r.WithContext(ctx))
	}
}

// roleMiddleware 校验当前用户角色，需在 authMiddleware 之后使用。
func roleMiddleware(next http.HandlerFunc, allowed ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		if user == nil {
			api.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "未登录或会话已失效")
			return
		}
		for _, role := range allowed {
			if user.Role == role {
				next(w, r)
				return
			}
		}
		api.WriteError(w, r, http.StatusForbidden, "forbidden", "当前角色无权访问")
	}
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return authMiddleware(s.auth, next)
}

func (s *Server) requireRole(next http.HandlerFunc, allowed ...string) http.HandlerFunc {
	return roleMiddleware(next, allowed...)
}
