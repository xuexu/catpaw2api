package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDoJSONEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte(`{"code":0,"data":{"userId":"u1","userName":"n1"}}`))
		case "/raw":
			_, _ = w.Write([]byte(`{"availableCredits":123}`))
		case "/number":
			_, _ = w.Write([]byte(`{"code":0,"data":{"userId":123456789,"userName":"n1"}}`))
		case "/err":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":4001,"message":"bad token"}`))
		}
	}))
	defer srv.Close()
	c := New(5 * time.Second)

	var u UserInfo
	if err := c.doJSON(context.Background(), http.MethodGet, srv.URL, "/ok", nil, nil, &u); err != nil {
		t.Fatal(err)
	}
	if u.UserID != "u1" || u.UserName != "n1" {
		t.Fatalf("got %+v", u)
	}

	// 测试 userId 为 number 的情况（修复的 bug）
	var unum UserInfo
	if err := c.doJSON(context.Background(), http.MethodGet, srv.URL, "/number", nil, nil, &unum); err != nil {
		t.Fatal(err)
	}
	if unum.UserID != "123456789" || unum.UserName != "n1" {
		t.Fatalf("got %+v", unum)
	}

	var bal map[string]any
	if err := c.doJSON(context.Background(), http.MethodGet, srv.URL, "/raw", nil, nil, &bal); err != nil {
		t.Fatal(err)
	}
	if ParseCredits(bal) != 123 {
		t.Fatalf("credits=%d", ParseCredits(bal))
	}

	err := c.doJSON(context.Background(), http.MethodGet, srv.URL, "/err", nil, nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "4001") {
		t.Fatalf("err=%v", err)
	}
}

// SendMessage：上游 data.success=false 时必须透传 errorCode/errorMessage，
// 而不是报笼统的 empty conversationId。
func TestSendMessageUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"","data":{"conversationId":null,"roundId":null,` +
			`"streamMessageId":"1","userMessageId":"2","assistantMessageId":"3",` +
			`"success":false,"errorCode":500,"errorMessage":"<mock upstream failure detail>"}}`))
	}))
	defer srv.Close()
	old := NocodeHost
	NocodeHost = srv.URL
	defer func() { NocodeHost = old }()

	c := New(time.Minute)
	_, err := c.SendMessage(context.Background(), "tok", "chat1", "hi", "glm-5.3-flash")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "upstream error") ||
		!strings.Contains(err.Error(), "code=500") ||
		!strings.Contains(err.Error(), "<mock upstream failure detail>") {
		t.Fatalf("err=%v should surface upstream errorCode/errorMessage", err)
	}
}

// 直连/聊天信封 {unifyCode,code,msg,data,success} 必须按 success 判定。
func TestDoJSONDirectEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte(`{"unifyCode":0,"code":0,"msg":"成功","data":{"id":"glm-5.2"},"success":true}`))
		case "/fail":
			_, _ = w.Write([]byte(`{"unifyCode":1005010001,"code":400,"msg":"tenant 不能为空","data":null,"success":false}`))
		}
	}))
	defer srv.Close()
	c := New(5 * time.Second)

	var got struct {
		ID string `json:"id"`
	}
	if err := c.doJSON(context.Background(), http.MethodPost, srv.URL, "/ok", nil, nil, &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "glm-5.2" {
		t.Fatalf("got id=%s", got.ID)
	}

	err := c.doJSON(context.Background(), http.MethodPost, srv.URL, "/fail", nil, nil, nil)
	if err == nil {
		t.Fatal("expected error on success=false")
	}
	if !strings.Contains(err.Error(), "tenant") {
		t.Fatalf("err=%v should contain tenant", err)
	}
}

// PollAssistant 必须轮询直到 assistant 出现且 finished=true。
func TestPollAssistant(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("Content-Type", "application/json")
		var data string
		if n < 3 {
			// 前两次只有 user 消息
			data = `{"unifyCode":0,"code":0,"msg":"成功","data":{"items":[{"type":"user","status":1,"finished":true,"content":[{"type":"text","text":"hi"}],"totalUsage":{"prompt_tokens":10,"completion_tokens":0,"total_tokens":10}}]},"success":true}`
		} else {
			data = `{"unifyCode":0,"code":0,"msg":"成功","data":{"items":[{"type":"user","status":1,"finished":true,"content":[{"type":"text","text":"hi"}],"totalUsage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}},{"type":"assistant","status":1,"finished":true,"content":[{"type":"text","text":"你好，有什么可以帮你的吗？"}]}]},"success":true}`
		}
		_, _ = w.Write([]byte(data))
	}))
	defer srv.Close()
	DirectHost = srv.URL

	c := New(5 * time.Second)
	res, err := c.PollAssistant(context.Background(), "tok", "uid", "conv-1", PollOpts{Interval: 10 * time.Millisecond, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "你好，有什么可以帮你的吗？" {
		t.Fatalf("content=%q", res.Content)
	}
	if res.Usage == nil || res.Usage["total_tokens"].(float64) != 15 {
		t.Fatalf("usage=%v", res.Usage)
	}
	if res.Finish != "stop" {
		t.Fatalf("finish=%s", res.Finish)
	}
	if n < 3 {
		t.Fatalf("should have polled at least 3 times, got %d", n)
	}
}

// PollAssistant 超时必须报错。
func TestPollAssistantTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"unifyCode":0,"code":0,"msg":"成功","data":{"items":[]},"success":true}`))
	}))
	defer srv.Close()
	DirectHost = srv.URL

	c := New(5 * time.Second)
	_, err := c.PollAssistant(context.Background(), "tok", "uid", "conv-2", PollOpts{Interval: 10 * time.Millisecond, Timeout: 100 * time.Millisecond})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("err=%v should mention timeout", err)
	}
}

