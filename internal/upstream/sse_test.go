package upstream

import (
	"strings"
	"testing"
)

func TestParseFrameStatusUpdate(t *testing.T) {
	f, err := parseFrame(`{"statusUpdate":"running"}`)
	if err != nil {
		t.Fatal(err)
	}
	if f == nil || f.status != "running" {
		t.Fatalf("got %+v", f)
	}
	f, _ = parseFrame(`{"statusUpdate":"completed"}`)
	if f == nil || f.status != "completed" {
		t.Fatalf("got %+v", f)
	}
}

func TestParseFrameHeadlessMessage(t *testing.T) {
	line := `{"headlessRemoteAgentResp":{"message":{"messageId":"m1","role":"assistant","finished":true,"content":[{"type":"text","text":"hello"},{"type":"reasoning","reasoning":"think"}]}}}`
	f, err := parseFrame(line)
	if err != nil {
		t.Fatal(err)
	}
	if f == nil {
		t.Fatal("nil frame")
	}
	if f.delta != "hello" || f.reason != "think" || !f.finished {
		t.Fatalf("got %+v", f)
	}
}

func TestParseFrameHeadlessError(t *testing.T) {
	f, err := parseFrame(`{"headlessRemoteAgentResp":{"error":"quota exhausted"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if f == nil || f.err == nil || f.err.Msg != "quota exhausted" {
		t.Fatalf("got %+v", f)
	}
}

func TestParseFrameModelChunk(t *testing.T) {
	f, err := parseFrame(`{"type":"model_chunk","content":"你","reasoningContent":"思","finished":false}`)
	if err != nil {
		t.Fatal(err)
	}
	if f.delta != "你" || f.reason != "思" {
		t.Fatalf("got %+v", f)
	}
}

func TestParseFrameAgentEndAndError(t *testing.T) {
	f, err := parseFrame(`{"type":"agent_end","reason":"completed"}`)
	if err != nil {
		t.Fatal(err)
	}
	if f.status != "completed" {
		t.Fatalf("got %+v", f)
	}
	f, err = parseFrame(`{"type":"agent_error","error":"boom","code":"500"}`)
	if err != nil {
		t.Fatal(err)
	}
	if f.err == nil || f.err.Msg != "boom" || f.err.Code != "500" {
		t.Fatalf("got %+v", f)
	}
}

func TestParseFrameEventWrapper(t *testing.T) {
	f, err := parseFrame(`{"event":{"type":"model_chunk","data":{"content":"x","reasoningContent":"r"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if f.delta != "x" || f.reason != "r" {
		t.Fatalf("got %+v", f)
	}
}

func TestParseFrameHistorySkipped(t *testing.T) {
	line := `{"headlessRemoteAgentResp":{"isHistoryMessage":true,"message":{"role":"assistant","content":[{"type":"text","text":"old"}]}}}`
	f, err := parseFrame(line)
	if err != nil {
		t.Fatal(err)
	}
	// parseMessage 提取文本；历史帧由上层语义判断，这里只验证可解析
	if f.delta != "old" {
		t.Fatalf("got %+v", f)
	}
}

func TestAggregate(t *testing.T) {
	stream := "data: {\"type\":\"model_chunk\",\"content\":\"hello \"}\n" +
		"data: {\"type\":\"model_chunk\",\"content\":\"world\",\"reasoningContent\":\"think\"}\n" +
		"data: {\"statusUpdate\":\"completed\"}\n\n"
	resp, err := Aggregate(strings.NewReader(stream), "test-model")
	if err != nil {
		t.Fatal(err)
	}
	msg := resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if msg["content"] != "hello world" {
		t.Fatalf("content=%v", msg["content"])
	}
	if msg["reasoning_content"] != "think" {
		t.Fatalf("reasoning=%v", msg["reasoning_content"])
	}
}

func TestAggregateError(t *testing.T) {
	stream := "data: {\"type\":\"agent_error\",\"error\":\"no quota\"}\n\n"
	_, err := Aggregate(strings.NewReader(stream), "m")
	if err == nil {
		t.Fatal("expected error")
	}
	var ue *UpstreamError
	if !strings.Contains(err.Error(), "no quota") {
		t.Fatalf("err=%v", err)
	}
	_ = ue
}
