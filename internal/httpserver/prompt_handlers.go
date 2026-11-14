package httpserver

import (
	"net/http"

	"github.com/hequan2017/bigmodel/internal/api"
	"github.com/hequan2017/bigmodel/internal/prompt"
)

// handleValidatePrompt 校验 Prompt 模板：语法与变量白名单（编辑器实时校验）。
// 始终返回 200，以 valid 字段表达结果，便于前端区分展示。
func (s *Server) handleValidatePrompt(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req struct {
		Template string `json:"template"`
	}
	if err := jsonDecode(r.Body, &req); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "请求体必须是 JSON")
		return
	}
	resp := map[string]any{
		"valid":     true,
		"variables": prompt.Variables(req.Template),
	}
	if err := prompt.Validate(req.Template); err != nil {
		resp["valid"] = false
		resp["error"] = err.Error()
	}
	api.WriteJSON(w, http.StatusOK, resp)
}

// handleRenderPrompt 调试渲染：草稿调试用预览，学生侧使用发布快照（§8.4）。
func (s *Server) handleRenderPrompt(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req struct {
		Template string `json:"template"`
		Question string `json:"question"`
		Context  string `json:"context"`
		History  string `json:"history"`
	}
	if err := jsonDecode(r.Body, &req); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "请求体必须是 JSON")
		return
	}

	if err := prompt.Validate(req.Template); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "invalid_template", err.Error())
		return
	}
	// 仅提交非空输入：未填写的样例变量按"缺失"处理，给教师明确提示。
	inputs := prompt.Inputs{}
	if req.Question != "" {
		inputs["question"] = req.Question
	}
	if req.Context != "" {
		inputs["context"] = req.Context
	}
	if req.History != "" {
		inputs["history"] = req.History
	}
	rendered, err := prompt.Render(req.Template, inputs)
	if err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "missing_variable", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"rendered": rendered})
}
