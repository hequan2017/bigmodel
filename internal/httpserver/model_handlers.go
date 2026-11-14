package httpserver

import (
	"errors"
	"net/http"

	"github.com/hequan2017/bigmodel/internal/api"
	"github.com/hequan2017/bigmodel/internal/model"
)

// handleListModels 列出模型配置；credential_ref 仅是环境变量名，响应不含密钥正文。
func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.models.ListProfiles(r.Context())
	if err != nil {
		s.log.Error("查询模型配置失败", "error", err)
		api.WriteError(w, r, http.StatusInternalServerError, "internal_error", "查询模型配置失败")
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{
		"models":               profiles,
		"chat_configured":      s.models.ChatConfigured(),
		"embedding_configured": s.models.EmbeddingConfigured(),
	})
}

// handleTestChat 真实调用一次 Chat 上游验证连通性。
func (s *Server) handleTestChat(w http.ResponseWriter, r *http.Request) {
	if !s.models.ChatConfigured() {
		api.WriteError(w, r, http.StatusConflict, "model_not_configured", "未配置 Chat 上游，请设置 APP_CHAT_* 环境变量")
		return
	}
	res, err := s.models.TestChat(r.Context())
	s.writeModelTestResult(w, r, res, err)
}

// handleTestEmbedding 真实调用一次 Embedding 上游并回写实测向量维度。
func (s *Server) handleTestEmbedding(w http.ResponseWriter, r *http.Request) {
	if !s.models.EmbeddingConfigured() {
		api.WriteError(w, r, http.StatusConflict, "model_not_configured", "未配置 Embedding 上游，请设置 APP_EMBEDDING_* 环境变量")
		return
	}
	res, err := s.models.TestEmbedding(r.Context())
	s.writeModelTestResult(w, r, res, err)
}

func (s *Server) writeModelTestResult(w http.ResponseWriter, r *http.Request, res map[string]any, err error) {
	if err != nil {
		var ue *model.UpstreamError
		if errors.As(err, &ue) {
			s.log.Warn("模型连通性测试失败", "code", ue.Code, "status", ue.Status, "message", ue.Message)
			api.WriteError(w, r, http.StatusBadGateway, ue.Code, ue.Message)
			return
		}
		s.log.Error("模型连通性测试异常", "error", err)
		api.WriteError(w, r, http.StatusInternalServerError, "internal_error", "模型测试失败")
		return
	}
	api.WriteJSON(w, http.StatusOK, res)
}
