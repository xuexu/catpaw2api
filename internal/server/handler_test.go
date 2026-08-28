package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"catpaw2api/internal/pool"
	"catpaw2api/internal/upstream"
)

func newTestHandler() *Handler {
	return &Handler{
		cfg:    Config{DefaultModel: "glm-5.2"},
		convs:  map[string]*convState{},
		latest: map[string]string{},
	}
}

func userMsgs(texts ...string) []openAIMessage {
	out := make([]openAIMessage, 0, len(texts))
	for _, t := range texts {
		out = append(out, openAIMessage{Role: "user", Text: t})
	}
	return out
}

func TestWithLogging(t *testing.T) {
	h := newTestHandler()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":"x"}`))
	})
	hh := h.withLogging(mux)

	// 404
	rec := httptest.NewRecorder()
	hh.ServeHTTP(rec, httptest.NewRequest("GET", "/nope", nil))
	if rec.Code != 404 {
		t.Fatalf("code=%d", rec.Code)
	}

	// 401 带响应体
	rec = httptest.NewRecorder()
	hh.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	if rec.Code != 401 || rec.Body.Len() == 0 {
		t.Fatalf("code=%d body=%d", rec.Code, rec.Body.Len())
	}

	// healthz 正常透传
	rec = httptest.NewRecorder()
	hh.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 {
		t.Fatalf("healthz code=%d", rec.Code)
	}
}

func TestValidateModel(t *testing.T) {
	h := newTestHandler() // Pool 为 nil → fetchModels 返回空

	// 动态模型表拉取失败时跳过严格校验（兜底表可能滞后于上游），
	// 未知模型也放行，交由上游判定。
	for _, m := range []string{"", "auto", "glm-5.3-flash", "some-future-model"} {
		if err := h.validateModel(m); err != nil {
			t.Fatalf("model %q should pass when dynamic list unavailable, got %v", m, err)
		}
	}

	// 动态模型表可用 → 严格校验
	dynamicModels.Lock()
	dynamicModels.list = []upstream.ModelInfo{{ModelTypeName: "glm-5.3-flash"}, {ModelTypeName: "kimi-k3"}}
	dynamicModels.fetched = time.Now()
	dynamicModels.Unlock()
	defer func() {
		dynamicModels.Lock()
		dynamicModels.list = nil
		dynamicModels.Unlock()
	}()

	for _, m := range []string{"", "AUTO", "glm-5.3-flash", "Kimi-K3"} {
		if err := h.validateModel(m); err != nil {
			t.Fatalf("model %q should pass, got %v", m, err)
		}
	}
	err := h.validateModel("glm-9.9-beta")
	if err == nil {
		t.Fatal("expected error for unknown model")
	}
	if !contains(err.Error(), "glm-9.9-beta") || !contains(err.Error(), "glm-5.3-flash") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPlanConversationNew(t *testing.T) {
	h := newTestHandler()
	req := &chatRequest{Messages: userMsgs("hi")}
	acct := &pool.Account{Name: "a"}
	conv, isNew, prompt, err := h.planConversation(acct, req, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if conv != nil || !isNew || prompt != "hi" {
		t.Fatalf("conv=%v isNew=%v prompt=%q", conv, isNew, prompt)
	}
}

func TestPlanConversationContinueAndReplay(t *testing.T) {
	h := newTestHandler()
	acct := &pool.Account{Name: "a"}

	// 第一轮：新会话
	first := &chatRequest{Messages: userMsgs("q1")}
	conv, isNew, _, err := h.planConversation(acct, first, nil, "")
	if err != nil || !isNew || conv != nil {
		t.Fatalf("unexpected: %+v isNew=%v err=%v", conv, isNew, err)
	}
	// 手动构造已吸收状态（模拟 driveConversation + finalizeConversation）
	conv = &convState{ChatID: "c1", ConversationID: "x1", Account: "a"}
	assistant := openAIMessage{Role: "assistant", Text: "a1"}
	conv.Absorbed = len(first.Messages) + 1
	conv.Fingerprint = chainFingerprint(fingerprintOf(first.Messages), assistant)
	h.convs["x1"] = conv
	h.latest["a"] = "x1"

	// 第二轮：客户端回发完整历史 + 新消息 → 指纹匹配 → 增量尾部
	second := &chatRequest{Messages: append(append([]openAIMessage{}, first.Messages...), assistant, openAIMessage{Role: "user", Text: "q2"})}
	conv2, isNew2, prompt2, err := h.planConversation(acct, second, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if isNew2 || conv2.ConversationID != "x1" || prompt2 != "q2" {
		t.Fatalf("isNew=%v conv=%s prompt=%q", isNew2, conv2.ConversationID, prompt2)
	}

	// 完全重放（无新消息）→ errNoNewMessage
	replay := &chatRequest{Messages: append(append([]openAIMessage{}, first.Messages...), assistant)}
	if _, _, _, err := h.planConversation(acct, replay, nil, ""); !errors.Is(err, errNoNewMessage) {
		t.Fatalf("expected errNoNewMessage, got %v", err)
	}

	// 历史被改写 → 指纹不匹配 → 新会话
	diverged := &chatRequest{Messages: append(append([]openAIMessage{}, first.Messages...), openAIMessage{Role: "assistant", Text: "different"}, openAIMessage{Role: "user", Text: "q3"})}
	conv3, isNew3, prompt3, err := h.planConversation(acct, diverged, nil, "")
	if err != nil || !isNew3 || conv3 != nil || !contains(prompt3, "q3") {
		t.Fatalf("conv=%v isNew=%v prompt=%q err=%v", conv3, isNew3, prompt3, err)
	}
}

func TestPlanConversationForced(t *testing.T) {
	h := newTestHandler()
	conv := &convState{ChatID: "c1", ConversationID: "x1", Account: "a", Absorbed: 1}
	req := &chatRequest{Messages: userMsgs("q1", "q2")}
	conv.Absorbed = 1
	conv.Fingerprint = fingerprintOf(req.Messages[:1])
	h.convs["x1"] = conv
	acct := &pool.Account{Name: "a"}
	out, isNew, prompt, err := h.planConversation(acct, req, conv, "")
	if err != nil || isNew || out != conv || prompt != "q2" {
		t.Fatalf("out=%v isNew=%v prompt=%q err=%v", out, isNew, prompt, err)
	}
}

func TestBuildCompletionShape(t *testing.T) {
	resp := buildCompletion("m1", "think", "hello", nil, "stop", "x1", map[string]int{"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3})
	choice := resp["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "stop" {
		t.Fatalf("finish=%v", choice["finish_reason"])
	}
	msg := choice["message"].(map[string]any)
	if msg["content"] != "hello" {
		t.Fatalf("content=%v", msg["content"])
	}
	if resp["conversation_id"] != "x1" {
		t.Fatalf("conv=%v", resp["conversation_id"])
	}
	// 工具调用形态
	resp2 := buildCompletion("m1", "", "", []openAIToolCall{{ID: "call_1", Name: "f1", Arguments: `{}`}}, "tool_calls", "x1", nil)
	msg2 := resp2["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if msg2["tool_calls"] == nil {
		t.Fatal("tool_calls missing")
	}
}

func TestMapModelExact(t *testing.T) {
	h := newTestHandler()
	// empty -> default
	if m := h.mapModel("", "glm-5.2"); m != "glm-5.2" {
		t.Fatalf("empty should use default, got %s", m)
	}
	// auto -> default
	if m := h.mapModel("auto", "glm-5.2"); m != "glm-5.2" {
		t.Fatalf("auto should use default, got %s", m)
	}
	// exact model passed through
	if m := h.mapModel("deepseek-v4-flash", "glm-5.2"); m != "deepseek-v4-flash" {
		t.Fatalf("exact model should be passed through unchanged, got %s", m)
	}
	// other alias removed (safety)
	if m := h.mapModel("lite", "glm-5.2"); m != "lite" {
		t.Fatalf("non-auto should remain exact, got %s", m)
	}
}

func TestModelListExact(t *testing.T) {
	h := newTestHandler()
	models := h.modelList()
	// 真实模型表（逆向自 app.asar，tenant=CatDesk,scene=CATX_APP,env=EXTERNAL 返回的 7 个）。
	expected := []string{
		"auto", "LongCat-2.0", "deepseek-v4-flash", "deepseek-v4-pro",
		"glm-5.2", "MiniMax-M3", "kimi-k3",
	}
	found := make(map[string]bool)
	for _, m := range models {
		id := m["id"].(string)
		found[id] = true
	}
	for _, e := range expected {
		if !found[e] {
			t.Fatalf("missing exact model %s", e)
		}
	}
}
