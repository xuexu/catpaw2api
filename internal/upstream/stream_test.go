package upstream

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStreamCaptureAccumulates(t *testing.T) {
	stream := "data: {\"type\":\"model_chunk\",\"content\":\"你\",\"reasoningContent\":\"思\"}\n" +
		"data: {\"type\":\"model_chunk\",\"content\":\"好\"}\n" +
		"data: {\"statusUpdate\":\"completed\"}\n\n"
	rec := httptest.NewRecorder()
	var captured *RawCompletion
	err := StreamCapture(rec, strings.NewReader(stream), "m", func(rc *RawCompletion) {
		captured = rc
	})
	if err != nil {
		t.Fatal(err)
	}
	if captured == nil {
		t.Fatal("capture callback not invoked")
	}
	if captured.Content != "你好" || captured.Reasoning != "思" {
		t.Fatalf("captured=%+v", captured)
	}
	if !strings.Contains(rec.Body.String(), "[DONE]") {
		t.Fatalf("missing [DONE]: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"content":"你"`) || !strings.Contains(rec.Body.String(), `"content":"好"`) {
		t.Fatalf("chunks missing: %s", rec.Body.String())
	}
}

func TestStreamCaptureErrorStream(t *testing.T) {
	stream := "data: {\"type\":\"model_chunk\",\"content\":\"x\"}\n" +
		"data: {\"type\":\"agent_error\",\"error\":\"boom\"}\n\n"
	rec := httptest.NewRecorder()
	var captured *RawCompletion
	err := StreamCapture(rec, bytes.NewBufferString(stream), "m", func(rc *RawCompletion) {
		captured = rc
	})
	if err != nil {
		t.Fatal(err)
	}
	if captured == nil || captured.Content != "x" {
		t.Fatalf("captured=%+v", captured)
	}
	if !strings.Contains(rec.Body.String(), "event: error") {
		t.Fatalf("error event missing: %s", rec.Body.String())
	}
}
