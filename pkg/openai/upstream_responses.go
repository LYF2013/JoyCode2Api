package openai

// Native upstream Responses API channel for adapterType "openai-response"
// models (GPT-5.6 Sol, GPT-6 Astra). These models are served by the color
// gateway functionId responses_completions as a real OpenAI Responses API:
// the request uses `input` (not `messages`), the model name is the label
// as-is (no lowercase-hyphen conversion), and the response is a nested SSE
// stream where each outer "data:" field wraps one inner SSE line.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/vibe-coding-labs/JoyCode2Api/pkg/joycode"
	"github.com/vibe-coding-labs/JoyCode2Api/pkg/store"
)

const responsesUpstreamEndpoint = "/api/saas/openai/v1/responses"

// ---------------------------------------------------------------------------
// Nested SSE unwrapping
// ---------------------------------------------------------------------------

// upstreamEvent is one event from the upstream Responses stream after
// unwrapping the gateway's nested SSE envelope.
type upstreamEvent struct {
	event string // inner "event:" name, "" if absent
	data  string // inner "data:" payload
}

// unwrapResponsesStream reads the gateway's nested SSE stream: every outer
// "data:" field carries one inner SSE line ("event: ...", "data: ...", or
// "[DONE]"). Non-nested payloads (plain JSON error bodies, unwrapped data
// lines) are passed through as data events.
func unwrapResponsesStream(body io.Reader, events chan<- upstreamEvent) {
	defer close(events)
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var pendingEvent string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		payload := line
		if strings.HasPrefix(line, "data: ") {
			payload = strings.TrimPrefix(line, "data: ")
		}
		// A payload that is itself a full SSE line is the nested form.
		switch {
		case strings.HasPrefix(payload, "event: "):
			pendingEvent = strings.TrimPrefix(payload, "event: ")
			continue
		case strings.HasPrefix(payload, "data: "):
			payload = strings.TrimPrefix(payload, "data: ")
		}
		events <- upstreamEvent{event: pendingEvent, data: payload}
		pendingEvent = ""
	}
	if err := scanner.Err(); err != nil {
		slog.Error("responses upstream stream read error", "error", err)
	}
}

// upstreamErrorMessage extracts an error message from an upstream payload,
// which may be a plain JSON error body ({"error":{"code":"6002",...}}) or a
// FAILED_RESPONSE SSE data payload. Returns "" if the payload is not an error.
func upstreamErrorMessage(data string) string {
	var env struct {
		Error *struct {
			Code    interface{} `json:"code"`
			Message string      `json:"message"`
		} `json:"error"`
	}
	if !strings.HasPrefix(data, "{") {
		return ""
	}
	if json.Unmarshal([]byte(data), &env) != nil || env.Error == nil {
		return ""
	}
	msg := env.Error.Message
	if msg == "" {
		msg = "upstream error"
	}
	if env.Error.Code != nil {
		return fmt.Sprintf("%v: %s", env.Error.Code, msg)
	}
	return msg
}

// ---------------------------------------------------------------------------
// Chat Completions → Responses request translation
// ---------------------------------------------------------------------------

// chatToResponsesBody translates an OpenAI Chat Completions request into the
// native Responses API body: system messages become instructions, messages
// become input items, tools flatten to the Responses function format.
// The upstream is always asked to stream; non-stream clients get an
// aggregated response.
func chatToResponsesBody(req *ChatRequest) map[string]interface{} {
	body := map[string]interface{}{
		"model":  req.Model,
		"stream": true,
	}

	var instructions []string
	var input []interface{}

	var msgs []map[string]interface{}
	if len(req.Messages) > 0 {
		json.Unmarshal(req.Messages, &msgs)
	}
	for _, m := range msgs {
		role, _ := m["role"].(string)
		switch role {
		case "system", "developer":
			if text := extractTextContent(m["content"]); text != "" {
				instructions = append(instructions, text)
			}
		case "user":
			input = append(input, map[string]interface{}{
				"type": "message", "role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "input_text", "text": extractTextContent(m["content"])},
				},
			})
		case "assistant":
			if text := extractTextContent(m["content"]); text != "" {
				input = append(input, map[string]interface{}{
					"type": "message", "role": "assistant",
					"content": []interface{}{
						map[string]interface{}{"type": "output_text", "text": text},
					},
				})
			}
			if tcs, ok := m["tool_calls"].([]interface{}); ok {
				for _, tc := range tcs {
					tcMap, _ := tc.(map[string]interface{})
					fn, _ := tcMap["function"].(map[string]interface{})
					input = append(input, map[string]interface{}{
						"type":      "function_call",
						"call_id":   tcMap["id"],
						"name":      fn["name"],
						"arguments": fn["arguments"],
					})
				}
			}
		case "tool":
			input = append(input, map[string]interface{}{
				"type":    "function_call_output",
				"call_id": m["tool_call_id"],
				"output":  extractTextContent(m["content"]),
			})
		}
	}

	if len(instructions) > 0 {
		body["instructions"] = strings.Join(instructions, "\n")
	}
	if len(input) > 0 {
		body["input"] = input
	}
	if req.MaxTokens > 0 {
		body["max_output_tokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if len(req.Tools) > 0 {
		if tools := chatToolsToResponses(req.Tools); len(tools) > 0 {
			body["tools"] = tools
		}
	}
	if len(req.ToolChoice) > 0 {
		if tc := chatToolChoiceToResponses(req.ToolChoice); tc != nil {
			body["tool_choice"] = tc
		}
	}
	return body
}

// extractTextContent normalizes a chat message content value (plain string or
// typed parts array) down to its concatenated text. Non-text parts (images)
// are dropped.
func extractTextContent(v interface{}) string {
	switch c := v.(type) {
	case string:
		return c
	case []interface{}:
		var parts []string
		for _, p := range c {
			pm, _ := p.(map[string]interface{})
			t, _ := pm["type"].(string)
			if t == "text" || t == "input_text" || t == "output_text" {
				if s, ok := pm["text"].(string); ok {
					parts = append(parts, s)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// chatToolsToResponses flattens Chat Completions tools
// ([{"type":"function","function":{...}}]) into the Responses API form
// ([{"type":"function","name":...,"description":...,"parameters":...}]).
func chatToolsToResponses(raw json.RawMessage) []interface{} {
	var tools []map[string]interface{}
	if json.Unmarshal(raw, &tools) != nil {
		return nil
	}
	out := make([]interface{}, 0, len(tools))
	for _, t := range tools {
		fn, _ := t["function"].(map[string]interface{})
		if fn == nil {
			continue
		}
		rt := map[string]interface{}{"type": "function", "name": fn["name"]}
		if d, ok := fn["description"].(string); ok && d != "" {
			rt["description"] = d
		}
		if p, ok := fn["parameters"]; ok {
			rt["parameters"] = p
		}
		out = append(out, rt)
	}
	return out
}

// chatToolChoiceToResponses maps Chat Completions tool_choice to the Responses
// API form. String values pass through; the function-object form is flattened.
func chatToolChoiceToResponses(raw json.RawMessage) interface{} {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj map[string]interface{}
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	if fn, ok := obj["function"].(map[string]interface{}); ok {
		return map[string]interface{}{"type": "function", "name": fn["name"]}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Responses events → Chat Completions
// ---------------------------------------------------------------------------

// responsesAccumulator collects a native Responses event stream into chat
// completion pieces: text, tool calls, usage.
type responsesAccumulator struct {
	text      strings.Builder
	toolCalls map[int]*toolCallState
	inTokens  int
	outTokens int
	completed bool
	errMsg    string
}

func newResponsesAccumulator() *responsesAccumulator {
	return &responsesAccumulator{toolCalls: map[int]*toolCallState{}}
}

// consume processes one upstream event. delta handlers are invoked as content
// arrives so streaming clients can forward it immediately.
func (a *responsesAccumulator) consume(ev upstreamEvent, onText func(string), onReasoning func(string), onToolCallStart func(idx int, id, name string), onToolCallArgs func(idx int, delta string)) {
	if ev.data == "" || ev.data == "[DONE]" {
		return
	}
	if msg := upstreamErrorMessage(ev.data); msg != "" {
		a.errMsg = msg
		return
	}

	eventType := ev.event
	if eventType == "" {
		var env struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(ev.data), &env) == nil {
			eventType = env.Type
		}
	}

	switch eventType {
	case "response.output_text.delta":
		var d struct {
			Delta string `json:"delta"`
		}
		if json.Unmarshal([]byte(ev.data), &d) == nil && d.Delta != "" {
			a.text.WriteString(d.Delta)
			if onText != nil {
				onText(d.Delta)
			}
		}
	case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
		var d struct {
			Delta string `json:"delta"`
		}
		if json.Unmarshal([]byte(ev.data), &d) == nil && d.Delta != "" {
			if onReasoning != nil {
				onReasoning(d.Delta)
			}
		}
	case "response.output_item.added":
		var d struct {
			OutputIndex int `json:"output_index"`
			Item        struct {
				Type   string `json:"type"`
				ID     string `json:"id"`
				CallID string `json:"call_id"`
				Name   string `json:"name"`
			} `json:"item"`
		}
		if json.Unmarshal([]byte(ev.data), &d) == nil && d.Item.Type == "function_call" {
			id := d.Item.CallID
			if id == "" {
				id = d.Item.ID
			}
			a.toolCalls[d.OutputIndex] = &toolCallState{index: d.OutputIndex, id: id, name: d.Item.Name}
			if onToolCallStart != nil {
				onToolCallStart(d.OutputIndex, id, d.Item.Name)
			}
		}
	case "response.function_call_arguments.delta":
		var d struct {
			OutputIndex int    `json:"output_index"`
			Delta       string `json:"delta"`
		}
		if json.Unmarshal([]byte(ev.data), &d) == nil && d.Delta != "" {
			st, ok := a.toolCalls[d.OutputIndex]
			if !ok {
				st = &toolCallState{index: d.OutputIndex}
				a.toolCalls[d.OutputIndex] = st
			}
			st.arguments += d.Delta
			if onToolCallArgs != nil {
				onToolCallArgs(d.OutputIndex, d.Delta)
			}
		}
	case "response.completed":
		var d struct {
			Response struct {
				Usage *struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(ev.data), &d) == nil {
			a.completed = true
			if d.Response.Usage != nil {
				a.inTokens = d.Response.Usage.InputTokens
				a.outTokens = d.Response.Usage.OutputTokens
			}
		}
	}
}

func (a *responsesAccumulator) finishReason() string {
	if len(a.toolCalls) > 0 {
		return "tool_calls"
	}
	return "stop"
}

// chatCompletionJSON builds a non-stream chat.completion response from the
// accumulated events.
func (a *responsesAccumulator) chatCompletionJSON(model string) map[string]interface{} {
	msg := map[string]interface{}{
		"role":    "assistant",
		"content": a.text.String(),
	}
	if len(a.toolCalls) > 0 {
		var tcs []interface{}
		for idx := 0; idx < len(a.toolCalls); idx++ {
			st, ok := a.toolCalls[idx]
			if !ok {
				continue
			}
			callID := st.id
			if callID == "" {
				callID = "call_" + newShortID()
			}
			tcs = append(tcs, map[string]interface{}{
				"id":   callID,
				"type": "function",
				"function": map[string]interface{}{
					"name":      st.name,
					"arguments": st.arguments,
				},
			})
		}
		msg["tool_calls"] = tcs
	}
	return map[string]interface{}{
		"id":      "chatcmpl-" + newShortID(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []interface{}{
			map[string]interface{}{
				"index":         0,
				"message":       msg,
				"finish_reason": a.finishReason(),
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     a.inTokens,
			"completion_tokens": a.outTokens,
			"total_tokens":      a.inTokens + a.outTokens,
		},
	}
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// startKeepalive launches the SSE heartbeat goroutine shared by the streaming
// handlers below.
func startKeepalive(w http.ResponseWriter, flusher http.Flusher) (stop chan struct{}, done chan struct{}) {
	stop = make(chan struct{})
	done = make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}()
	return stop, done
}

// openUpstreamResponses connects to the native Responses endpoint and returns
// the unwrapped event channel. On failure it returns an error message suitable
// for surfacing to the client.
func openUpstreamResponses(client *joycode.Client, body map[string]interface{}, model string) (<-chan upstreamEvent, func(), string) {
	resp, err := client.PostStream(responsesUpstreamEndpoint, body)
	if err != nil {
		slog.Error("native responses upstream error", "model", model, "error", err)
		msg := err.Error()
		if isTimeoutError(msg) {
			msg = "上游服务响应超时，请稍后重试。原始错误: " + msg
		}
		return nil, nil, msg
	}
	events := make(chan upstreamEvent, 64)
	go unwrapResponsesStream(resp.Body, events)
	return events, func() { resp.Body.Close() }, ""
}

// handleChatViaNativeResponses serves /v1/chat/completions (stream and
// non-stream) for openai-response adapter models over the native Responses
// channel.
func (s *Server) handleChatViaNativeResponses(w http.ResponseWriter, r *http.Request, client *joycode.Client, req *ChatRequest, model string) {
	jcBody := chatToResponsesBody(req)
	if req.Stream {
		s.handleChatNativeResponsesStream(w, r, client, jcBody, model)
	} else {
		s.handleChatNativeResponsesNonStream(w, r, client, jcBody, model)
	}
}

func (s *Server) handleChatNativeResponsesNonStream(w http.ResponseWriter, r *http.Request, client *joycode.Client, jcBody map[string]interface{}, model string) {
	events, cleanup, errMsg := openUpstreamResponses(client, jcBody, model)
	if errMsg != "" {
		writeError(w, 500, errMsg)
		return
	}
	defer cleanup()

	acc := newResponsesAccumulator()
	for ev := range events {
		acc.consume(ev, nil, nil, nil, nil)
	}
	if acc.errMsg != "" {
		writeError(w, 502, acc.errMsg)
		return
	}
	if acc.inTokens > 0 || acc.outTokens > 0 {
		store.SetTokenUsage(r, acc.inTokens, acc.outTokens)
	}
	writeJSON(w, 200, acc.chatCompletionJSON(model))
}

func (s *Server) handleChatNativeResponsesStream(w http.ResponseWriter, r *http.Request, client *joycode.Client, jcBody map[string]interface{}, model string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		slog.Error("streaming not supported by response writer")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "close")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(200)

	stopHeartbeat, heartbeatDone := startKeepalive(w, flusher)

	streamStart := time.Now()
	events, cleanup, errMsg := openUpstreamResponses(client, jcBody, model)
	if errMsg != "" {
		close(stopHeartbeat)
		<-heartbeatDone
		fmt.Fprintf(w, "data: {\"error\":{\"message\":%s}}\n\n", jsonString(errMsg))
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}
	defer cleanup()
	close(stopHeartbeat)
	<-heartbeatDone
	slog.Info("stream: connected to upstream", "model", model, "ttfb_ms", time.Since(streamStart).Milliseconds())

	chunkID := "chatcmpl-" + newShortID()
	created := time.Now().Unix()
	writeChunk := func(delta map[string]interface{}, finishReason *string, usage map[string]interface{}) {
		chunk := map[string]interface{}{
			"id":      chunkID,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []interface{}{
				map[string]interface{}{"index": 0, "delta": delta, "finish_reason": finishReason},
			},
		}
		if usage != nil {
			chunk["usage"] = usage
		}
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	acc := newResponsesAccumulator()
	roleSent := false
	ensureRole := func() {
		if !roleSent {
			writeChunk(map[string]interface{}{"role": "assistant"}, nil, nil)
			roleSent = true
		}
	}
	// Chat tool_call deltas use a dense 0-based index; upstream output_index
	// may not be dense when text items are interleaved.
	toolIdxMap := map[int]int{}
	mapToolIdx := func(outputIndex int) int {
		if i, ok := toolIdxMap[outputIndex]; ok {
			return i
		}
		i := len(toolIdxMap)
		toolIdxMap[outputIndex] = i
		return i
	}

	for ev := range events {
		acc.consume(ev,
			func(text string) {
				ensureRole()
				writeChunk(map[string]interface{}{"content": text}, nil, nil)
			},
			func(text string) {
				ensureRole()
				writeChunk(map[string]interface{}{"reasoning_content": text}, nil, nil)
			},
			func(outputIndex int, id, name string) {
				ensureRole()
				writeChunk(map[string]interface{}{
					"tool_calls": []interface{}{
						map[string]interface{}{
							"index":    mapToolIdx(outputIndex),
							"id":       id,
							"type":     "function",
							"function": map[string]interface{}{"name": name, "arguments": ""},
						},
					},
				}, nil, nil)
			},
			func(outputIndex int, delta string) {
				writeChunk(map[string]interface{}{
					"tool_calls": []interface{}{
						map[string]interface{}{
							"index":    mapToolIdx(outputIndex),
							"function": map[string]interface{}{"arguments": delta},
						},
					},
				}, nil, nil)
			},
		)
		if acc.errMsg != "" {
			break
		}
	}

	if acc.errMsg != "" {
		fmt.Fprintf(w, "data: {\"error\":{\"message\":%s}}\n\n", jsonString(acc.errMsg))
		flusher.Flush()
	} else {
		ensureRole()
		finish := acc.finishReason()
		usage := map[string]interface{}{
			"prompt_tokens":     acc.inTokens,
			"completion_tokens": acc.outTokens,
			"total_tokens":      acc.inTokens + acc.outTokens,
		}
		writeChunk(map[string]interface{}{}, &finish, usage)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()

	if acc.inTokens > 0 || acc.outTokens > 0 {
		store.SetTokenUsage(r, acc.inTokens, acc.outTokens)
	}
}

// handleResponsesNative serves /v1/responses for openai-response adapter
// models: the request is already in Responses format and only needs the model
// label plus upstream envelope fields; the unwrapped upstream events are
// forwarded as standard SSE.
func (s *Server) handleResponsesNative(w http.ResponseWriter, r *http.Request, client *joycode.Client, req *ResponsesRequest, model string) {
	body := map[string]interface{}{
		"model":  model,
		"stream": true,
	}
	if req.Instructions != "" {
		body["instructions"] = req.Instructions
	}
	if len(req.Input) > 0 {
		body["input"] = json.RawMessage(req.Input)
	}
	if len(req.Tools) > 0 {
		body["tools"] = json.RawMessage(req.Tools)
	}
	if len(req.ToolChoice) > 0 {
		body["tool_choice"] = json.RawMessage(req.ToolChoice)
	}
	if req.MaxOutput > 0 {
		body["max_output_tokens"] = req.MaxOutput
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}

	if req.Stream {
		s.handleResponsesNativeStream(w, r, client, body, model)
	} else {
		s.handleResponsesNativeNonStream(w, r, client, body, model)
	}
}

func (s *Server) handleResponsesNativeNonStream(w http.ResponseWriter, r *http.Request, client *joycode.Client, body map[string]interface{}, model string) {
	events, cleanup, errMsg := openUpstreamResponses(client, body, model)
	if errMsg != "" {
		writeError(w, 500, errMsg)
		return
	}
	defer cleanup()

	acc := newResponsesAccumulator()
	var completedResp json.RawMessage
	for ev := range events {
		acc.consume(ev, nil, nil, nil, nil)
		if ev.event == "response.completed" || strings.Contains(ev.data, `"type":"response.completed"`) {
			var env struct {
				Response json.RawMessage `json:"response"`
			}
			if json.Unmarshal([]byte(ev.data), &env) == nil && len(env.Response) > 0 {
				completedResp = env.Response
			}
		}
	}
	if acc.errMsg != "" {
		writeError(w, 502, acc.errMsg)
		return
	}
	if acc.inTokens > 0 || acc.outTokens > 0 {
		store.SetTokenUsage(r, acc.inTokens, acc.outTokens)
	}
	if len(completedResp) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(200)
		w.Write(completedResp)
		return
	}
	// Upstream ended without response.completed; synthesize from accumulated text.
	writeJSON(w, 200, map[string]interface{}{
		"id":         "resp_" + newShortID(),
		"object":     "response",
		"created_at": time.Now().Unix(),
		"status":     "completed",
		"model":      model,
		"output":     buildFinalOutput("msg_"+newShortID(), acc.text.String(), acc.toolCalls),
		"usage": map[string]interface{}{
			"input_tokens":  acc.inTokens,
			"output_tokens": acc.outTokens,
			"total_tokens":  acc.inTokens + acc.outTokens,
		},
	})
}

func (s *Server) handleResponsesNativeStream(w http.ResponseWriter, r *http.Request, client *joycode.Client, body map[string]interface{}, model string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		slog.Error("streaming not supported by response writer")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "close")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(200)

	stopHeartbeat, heartbeatDone := startKeepalive(w, flusher)

	streamStart := time.Now()
	events, cleanup, errMsg := openUpstreamResponses(client, body, model)
	close(stopHeartbeat)
	<-heartbeatDone
	if errMsg != "" {
		writeSSEEvent(w, "error", map[string]interface{}{"message": errMsg})
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}
	defer cleanup()
	slog.Info("stream: connected to upstream", "model", model, "ttfb_ms", time.Since(streamStart).Milliseconds())

	respID := "resp_" + newShortID()
	acc := newResponsesAccumulator()
	for ev := range events {
		acc.consume(ev, nil, nil, nil, nil)
		if ev.data == "" {
			continue
		}
		if msg := upstreamErrorMessage(ev.data); msg != "" {
			writeSSEEvent(w, "error", map[string]interface{}{"message": msg})
			flusher.Flush()
			writeSSEEvent(w, "response.completed", map[string]interface{}{
				"response": map[string]interface{}{
					"id": respID, "object": "response", "status": "failed", "model": model,
					"output": []interface{}{}, "usage": map[string]int{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0},
				},
			})
			flusher.Flush()
			break
		}
		if ev.data == "[DONE]" {
			break
		}
		name := ev.event
		if name == "" {
			var env struct {
				Type string `json:"type"`
			}
			if json.Unmarshal([]byte(ev.data), &env) == nil {
				name = env.Type
			}
		}
		if name == "" {
			continue
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, ev.data)
		flusher.Flush()
	}

	if !acc.completed && acc.errMsg == "" {
		slog.Info("native responses stream ended without response.completed, synthesizing",
			"model", model, "text_len", acc.text.Len(), "tool_calls", len(acc.toolCalls))
		writeSSEEvent(w, "response.completed", map[string]interface{}{
			"response": map[string]interface{}{
				"id": respID, "object": "response", "status": "completed", "model": model,
				"output": buildFinalOutput("msg_"+newShortID(), acc.text.String(), acc.toolCalls),
				"usage": map[string]interface{}{
					"input_tokens":  acc.inTokens,
					"output_tokens": acc.outTokens,
					"total_tokens":  acc.inTokens + acc.outTokens,
				},
			},
		})
		flusher.Flush()
	}

	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()

	if acc.inTokens > 0 || acc.outTokens > 0 {
		store.SetTokenUsage(r, acc.inTokens, acc.outTokens)
	}
}

// jsonString marshals s as a JSON string literal (quoted, escaped).
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
