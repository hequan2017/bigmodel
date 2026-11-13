package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hequan2017/bigmodel/internal/auth"
	"github.com/hequan2017/bigmodel/internal/config"
)

type fakeAuthenticator struct {
	user *auth.User
	err  error
}

func (f fakeAuthenticator) Authenticate(_ context.Context, _ string) (*auth.User, error) {
	return f.user, f.err
}

func testServer(t *testing.T) *Server {
	t.Helper()
	return &Server{
		cfg: config.Config{DataDir: t.TempDir()},
		log: slog.New(slog.NewJSONHandler(io.Discard, nil)),
	}
}

func TestAuthMiddleware(t *testing.T) {
	ok := func(w http.ResponseWriter, r *http.Request) {
		if auth.UserFromContext(r.Context()) == nil {
			t.Error("handler 应能从 context 取到用户")
		}
		if auth.TokenFromContext(r.Context()) == "" {
			t.Error("handler 应能从 context 取到令牌")
		}
		w.WriteHeader(http.StatusOK)
	}

	rr := httptest.NewRecorder()
	authMiddleware(fakeAuthenticator{err: auth.ErrUnauthorized}, ok)(rr, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("无令牌应 401, got %d", rr.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	rr = httptest.NewRecorder()
	authMiddleware(fakeAuthenticator{err: auth.ErrUnauthorized}, ok)(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("无效令牌应 401, got %d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer valid-token")
	rr = httptest.NewRecorder()
	authMiddleware(fakeAuthenticator{user: &auth.User{ID: 7, Role: auth.RoleAdmin}}, ok)(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("有效令牌应 200, got %d", rr.Code)
	}
}

func TestRoleMiddleware(t *testing.T) {
	next := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

	rr := httptest.NewRecorder()
	roleMiddleware(next, auth.RoleAdmin)(rr, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("无用户 context 应 401, got %d", rr.Code)
	}

	ctx := auth.WithUser(context.Background(), &auth.User{ID: 1, Role: auth.RoleTeacher})
	req := httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(ctx)
	rr = httptest.NewRecorder()
	roleMiddleware(next, auth.RoleAdmin)(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("teacher 访问 admin 路由应 403, got %d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(ctx)
	rr = httptest.NewRecorder()
	roleMiddleware(next, auth.RoleAdmin, auth.RoleTeacher)(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("允许角色应 200, got %d", rr.Code)
	}
}
