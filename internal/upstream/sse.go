// /api/agent/stream/connect SSE 帧 → OpenAI chat.completion 转换。
//
// CatPaw 直连流的 data 行是 JSON，存在三种形态（逆向自 catpaw-transform-util）：
//
//  1. 生命周期：{"statusUpdate":"running"|"completed"|"cancel"|"error"|...}
//  2. 包装帧：{"headlessRemoteAgentResp":{message:{...}}}
//     {"headlessRemoteAgentResp":{error:"..."}}
//     message.content: [{type:"text",text}, {type:"reasoning",reasoning}, {type:"tool_use",...}]
//  3. 事件帧：{"type":"model_chunk","content":"增量","reasoningContent":"增量","messageId":...,"finished":bool}
//     {"type":"agent_end","reason":"completed"}
//     {"type":"agent_error","error":"...","code":"..."}
//     {"event":{"type":"...","data":{...}}} 或 {"type":...,"data":{...}}
package upstream

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// UpstreamError 是直连流内的业务错误。
type UpstreamError struct {
	Code string
	Msg  string
}

func (e *UpstreamError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("catpaw error code=%s msg=%s", e.Code, e.Msg)
	}
	return fmt.Sprintf("catpaw error: %s", e.Msg)
}

// frame 是归一化后的单帧事件。
type frame struct {
	status   string // running/completed/cancel/error
	delta    string // content 增量
	reason   string // reasoning 增量
	finished bool
	err      *UpstreamError
}

// parseFrame 把一条 data 行解析为归一化事件。
func parseFrame(line string) (*frame, error) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	// 1. 生命周期帧
	if s, ok := raw["statusUpdate"].(string); ok && s != "" {
		return &frame{status: normalizeStatus(s)}, nil
	}
	// 2. 顶层 error（无 type/event）
	if _, hasType := raw["type"]; !hasType {
		if _, hasEvent := raw["event"]; !hasEvent {
			if e, ok := raw["error"]; ok {
				return &frame{err: &UpstreamError{Msg: errText(e)}}, nil
			}
		}
	}
	// headless 包装帧
	if inner, ok := unwrapHeadless(raw); ok {
		if e, ok := inner["error"]; ok && e != nil {
			return &frame{err: &UpstreamError{Msg: errText(e)}}, nil
		}
		if msg, ok := inner["message"].(map[string]any); ok {
			f, err := parseMessage(msg)
			if err != nil {
				return nil, err
			}
			return f, nil
		}
		if s, ok := inner["currentStatus"].(string); ok && s != "" {
			return &frame{status: normalizeStatus(s)}, nil
		}
		return nil, nil
	}
	// 3. 事件帧（含嵌套 event/data 包装）
	evt := normalizeEvent(raw)
	if evt == nil {
		return nil, nil
	}
	t, _ := evt["type"].(string)
	switch t {
	case "model_chunk", "delta":
		f := &frame{}
		f.delta, _ = evt["content"].(string)
		if r, ok := evt["reasoningContent"].(string); ok {
			f.reason = r
		} else if r, ok := evt["reasoning"].(string); ok {
			f.reason = r
		}
		if v, ok := evt["finished"].(bool); ok {
			f.finished = v
		}
		return f, nil
	case "agent_end", "turn_end", "model_end":
		reason, _ := evt["reason"].(string)
		if reason == "" {
			reason, _ = evt["status"].(string)
		}
		reason = strings.ToLower(strings.TrimSpace(reason))
		switch reason {
		case "running", "streaming", "":
			return &frame{status: "running"}, nil
		case "cancel", "canceled", "cancelled", "user_stopped":
			return &frame{status: "completed"}, nil
		case "error", "failed", "terminated":
			return &frame{err: &UpstreamError{Msg: "agent terminated"}}, nil
		default:
			return &frame{status: "completed"}, nil
		}
	case "agent_error":
		return &frame{err: &UpstreamError{Code: str(evt["code"]), Msg: errText(evt["error"])}}, nil
	case "status_update":
		s, _ := evt["status"].(string)
		return &frame{status: normalizeStatus(s)}, nil
	default:
		// agent_start / model_start / turn_start / user_message / tool_* 等：忽略
		return nil, nil
	}
}

// unwrapHeadless 解包 headlessRemoteAgentResp / headlessBackgroundAgentResp。
func unwrapHeadless(raw map[string]any) (map[string]any, bool) {
	for _, key := range []string{"headlessRemoteAgentResp", "headlessBackgroundAgentResp"} {
		if v, ok := raw[key].(map[string]any); ok {
			return v, true
		}
	}
	return nil, false
}

// normalizeEvent 兼容 {event:{type,data}} / {type,data} / {type,...} 包装。
func normalizeEvent(raw map[string]any) map[string]any {
	if ev, ok := raw["event"].(map[string]any); ok {
		if d, ok := ev["data"].(map[string]any); ok {
			if t, ok := d["type"].(string); ok {
				return merge(d, map[string]any{"type": t})
			}
		}
		if t, ok := ev["type"].(string); ok {
			// type 在 event 层，字段在 data 层 → 拍平
			out := map[string]any{"type": t}
			if d, ok := ev["data"].(map[string]any); ok {
				for k, v := range d {
					out[k] = v
				}
			}
			return out
		}
	}
	if _, ok := raw["type"].(string); ok {
		if d, ok := raw["data"].(map[string]any); ok {
			if t2, ok := d["type"].(string); ok {
				return merge(d, map[string]any{"type": t2})
			}
		}
		return raw
	}
	return nil
}

func merge(base, extra map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// parseMessage 解析 headless 包装里的 message：提取 text / reasoning / finished。
func parseMessage(msg map[string]any) (*frame, error) {
	content, _ := msg["content"].([]any)
	f := &frame{}
	var parts []string
	var reasonParts []string
	for _, ci := range content {
		item, ok := ci.(map[string]any)
		if !ok {
			continue
		}
		switch item["type"] {
		case "text":
			if s, ok := item["text"].(string); ok {
				parts = append(parts, s)
			}
		case "reasoning":
			if s, ok := item["reasoning"].(string); ok {
				reasonParts = append(reasonParts, s)
			} else if s, ok := item["text"].(string); ok {
				reasonParts = append(reasonParts, s)
			}
		}
	}
	f.delta = strings.Join(parts, "")
	f.reason = strings.Join(reasonParts, "")
	if v, ok := msg["finished"].(bool); ok {
		f.finished = v
	}
	return f, nil
}

func normalizeStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "running", "streaming", "idle":
		return "running"
	case "completed", "success", "finished", "done", "ok", "cancel", "canceled", "cancelled", "user_stopped":
		return "completed"
	case "error", "failed", "failure", "terminated":
		return "error"
	}
	return "running"
}

func errText(v any) string {
	switch t := v.(type) {
	case nil:
		return "unknown error"
	case string:
		if t == "" {
			return "unknown error"
		}
		return t
	case map[string]any:
		if m, ok := t["message"].(string); ok && m != "" {
			return m
		}
		if m, ok := t["msg"].(string); ok && m != "" {
			return m
		}
		return "unknown error"
	default:
		return fmt.Sprint(v)
	}
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if f, ok := v.(float64); ok {
		return fmt.Sprintf("%d", int64(f))
	}
	return ""
}

// ---------------------------------------------------------------------------
// 流式 / 聚合
// ---------------------------------------------------------------------------

// RawCompletion 是聚合后的原始结果（供网关做工具解析等二次加工）。
type RawCompletion struct {
	Content   string
	Reasoning string
	Finish    string
}

// AggregateRaw 读取完整 SSE，返回结构化聚合结果。
func AggregateRaw(r io.Reader) (*RawCompletion, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	var (
		content strings.Builder
		reason  strings.Builder
		finish  = "stop"
		upErr   error
	)
	for {
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "data: ") {
			f, perr := parseFrame(strings.TrimPrefix(line, "data: "))
			if perr != nil {
				return nil, fmt.Errorf("sse parse: %w", perr)
			}
			if f == nil {
				continue
			}
			content.WriteString(f.delta)
			reason.WriteString(f.reason)
			if f.finished {
				finish = "stop"
			}
			if f.status == "completed" {
				finish = "stop"
			}
			if f.status == "error" {
				upErr = &UpstreamError{Msg: "stream ended with error status"}
			}
			if f.err != nil {
				upErr = f.err
			}
		}
		if err == io.EOF {
			break
		}
	}
	if upErr != nil {
		return nil, upErr
	}
	return &RawCompletion{Content: content.String(), Reasoning: reason.String(), Finish: finish}, nil
}

// Aggregate 读取完整 SSE，聚合文本产出单个 OpenAI chat.completion。
func Aggregate(r io.Reader, model string) (map[string]any, error) {
	rc, err := AggregateRaw(r)
	if err != nil {
		return nil, err
	}
	message := map[string]any{"role": "assistant", "content": rc.Content}
	if rc.Reasoning != "" {
		message["reasoning_content"] = rc.Reasoning
	}
	return map[string]any{
		"id":      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{
			map[string]any{"index": 0, "message": message, "finish_reason": rc.Finish},
		},
	}, nil
}

// Stream 把 CatPaw SSE 实时转换为 OpenAI SSE，保证至少一个 [DONE]。
func Stream(w http.ResponseWriter, r io.Reader, model string) error {
	return streamWithCapture(w, r, model, nil)
}

// StreamCapture 同 Stream，额外在流正常结束时回调聚合出的完整内容/思考/结束原因，
// 供网关侧做会话指纹对齐等二次加工。onDone 只在无写入错误且流到达终态时调用一次。
func StreamCapture(w http.ResponseWriter, r io.Reader, model string, onDone func(*RawCompletion)) error {
	return streamWithCapture(w, r, model, onDone)
}

func streamWithCapture(w http.ResponseWriter, r io.Reader, model string, onDone func(*RawCompletion)) error {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	fl, _ := w.(http.Flusher)

	br := bufio.NewReaderSize(r, 64*1024)
	id := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	sawDone := false
	var (
		capturedContent strings.Builder
		capturedReason  strings.Builder
		capturedFinish  = "stop"
		streamErr       error
	)

	writeChunk := func(delta map[string]any, finish string) error {
		choice := map[string]any{"index": 0, "delta": delta}
		if finish != "" {
			choice["finish_reason"] = finish
		}
		chunk := map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   model,
			"choices": []any{choice},
		}
		raw, _ := json.Marshal(chunk)
		if _, err := io.WriteString(w, "data: "+string(raw)+"\n\n"); err != nil {
			return err
		}
		if fl != nil {
			fl.Flush()
		}
		return nil
	}
	writeDONE := func() error {
		if _, err := io.WriteString(w, "data: [DONE]\n\n"); err != nil {
			return err
		}
		if fl != nil {
			fl.Flush()
		}
		return nil
	}
	writeErr := func(msg string) error {
		payload := map[string]any{"error": map[string]any{"message": msg, "type": "upstream_error", "code": "CATPAW_STREAM_ERROR"}}
		raw, _ := json.Marshal(payload)
		if _, err := io.WriteString(w, "event: error\n"+"data: "+string(raw)+"\n\n"); err != nil {
			return err
		}
		if fl != nil {
			fl.Flush()
		}
		return nil
	}

	for {
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			streamErr = err
			break
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "data: ") {
			f, perr := parseFrame(strings.TrimPrefix(line, "data: "))
			if perr != nil {
				streamErr = fmt.Errorf("sse parse: %w", perr)
				break
			}
			if f == nil {
				if err == io.EOF {
					break
				}
				continue
			}
			capturedContent.WriteString(f.delta)
			capturedReason.WriteString(f.reason)
			if f.err != nil {
				if werr := writeErr(f.err.Error()); werr != nil {
					streamErr = werr
					break
				}
				if werr := writeDONE(); werr != nil {
					streamErr = werr
					break
				}
				sawDone = true
			} else {
				delta := map[string]any{}
				if f.delta != "" {
					delta["content"] = f.delta
				}
				if f.reason != "" {
					delta["reasoning_content"] = f.reason
				}
				if len(delta) > 0 {
					if werr := writeChunk(delta, ""); werr != nil {
						streamErr = werr
						break
					}
				}
				if f.status == "completed" {
					if werr := writeChunk(map[string]any{}, "stop"); werr != nil {
						streamErr = werr
						break
					}
					if werr := writeDONE(); werr != nil {
						streamErr = werr
						break
					}
					sawDone = true
				}
				if f.status == "error" {
					if werr := writeErr("stream ended with error status"); werr != nil {
						streamErr = werr
						break
					}
					if werr := writeDONE(); werr != nil {
						streamErr = werr
						break
					}
					sawDone = true
				}
			}
		}
		if err == io.EOF {
			break
		}
	}
	if streamErr == nil && !sawDone {
		streamErr = writeDONE()
	}
	if streamErr == nil && onDone != nil {
		onDone(&RawCompletion{Content: capturedContent.String(), Reasoning: capturedReason.String(), Finish: capturedFinish})
	}
	return streamErr
}
