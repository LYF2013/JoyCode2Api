package openai

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/vibe-coding-labs/JoyCode2Api/pkg/joycode"
	"github.com/vibe-coding-labs/JoyCode2Api/pkg/store"
)

// ---------------------------------------------------------------------------
// Types: Responses API request/response shapes
// ---------------------------------------------------------------------------

// ResponsesRequest is the OpenAI Responses API request body.
type ResponsesRequest struct {
	Model         string          `json:"model"`
	Instructions  string          `json:"instructions,omitempty"`
	Input         json.RawMessage `json:"input,omitempty"`
	Tools         json.RawMessage `json:"tools,omitempty"`
	ToolChoice    json.RawMessage `json:"tool_choice,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
	MaxOutput     int             `json:"max_output_tokens,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	ParallelTools bool            `json:"parallel_tool_calls,omitempty"`
	// Fields we ignore: store, include, reasoning, prompt_cache_key, client_metadata
}

// responsesInputItem represents one entry in the Responses API "input" array.
type responsesInputItem struct {
	Type    string             `json:"type"`              // "message"
	Role    string             `json:"role"`              // "user", "assistant", "developer", "system"
	Content []responsesContent `json:"content,omitempty"` // for type=message
}

// responsesContent is one content part in an input message.
type responsesContent struct {
	Type string `json:"type"` // "input_text", "output_text"
	Text string `json:"text"`
}

// responsesTool is a tool definition in the Responses API format.
type responsesTool struct {
	Type        string          `json:"type"`        // "function", "web_search", etc.
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var req ResponsesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Error("decode responses request", "error", err)
		writeError(w, 400, fmt.Sprintf("请求体解析失败: %s", err.Error()))
		return
	}

	systemDefault := ""
	if s.store != nil {
		systemDefault = s.store.GetSetting("default_model")
	}
	model := ResolveModel(req.Model, store.GetAccountDefaultModel(r), systemDefault)
	store.SetModel(r, model)

	client := s.getClient(r)
	adapterType := s.getAdapterType(client, model)

	// For openai-response adapter models, the upstream API expects the model
	// name in lowercase with spaces replaced by hyphens (e.g. "GPT-5.6 Sol" → "gpt-5.6-sol").
	if adapterType == "openai-response" {
		converted := strings.ToLower(strings.ReplaceAll(model, " ", "-"))
		req.Model = converted
		model = converted
	}

	slog.Info("responses request",
		"model", req.Model, "resolved", model, "stream", req.Stream, "adapter", adapterType)

	// Default path: translate Responses API → Chat Completions, then convert back.
	chatBody := translateResponsesToChat(&req)
	if req.Stream {
		s.handleResponsesStream(w, r, client, chatBody, model)
	} else {
		s.handleResponsesNonStream(w, r, client, chatBody, model)
	}
}

// ---------------------------------------------------------------------------
// Request translation: Responses API → Chat Completions
// ---------------------------------------------------------------------------

func translateResponsesToChat(req *ResponsesRequest) map[string]interface{} {
	body := map[string]interface{}{
		"model":  req.Model,
		"stream": req.Stream,
	}

	// Build messages: instructions → system message, then input items.
	messages := make([]interface{}, 0, 8)

	if req.Instructions != "" {
		messages = append(messages, map[string]interface{}{
			"role":    "system",
			"content": req.Instructions,
		})
	}

	if len(req.Input) > 0 {
		// The Responses API allows "input" to be either a plain string or
		// an array of input items. Handle both forms.
		var inputStr string
		if err := json.Unmarshal(req.Input, &inputStr); err == nil {
			messages = append(messages, map[string]interface{}{
				"role":    "user",
				"content": inputStr,
			})
		} else {
			var items []responsesInputItem
			if err := json.Unmarshal(req.Input, &items); err == nil {
				for _, item := range items {
					msg := translateInputItem(item)
					if msg != nil {
						messages = append(messages, msg)
					}
				}
			}
		}
	}

	if len(messages) > 0 {
		body["messages"] = messages
	}

	// Tools: Responses format → Chat Completions format
	if len(req.Tools) > 0 {
		var rTools []responsesTool
		if err := json.Unmarshal(req.Tools, &rTools); err == nil {
			chatTools := make([]interface{}, 0, len(rTools))
			for _, t := range rTools {
				if t.Type != "function" {
					continue // skip non-function tools (web_search, namespace, etc.)
				}
				fn := map[string]interface{}{
					"name": t.Name,
				}
				if t.Description != "" {
					fn["description"] = t.Description
				}
				if len(t.Parameters) > 0 {
					fn["parameters"] = json.RawMessage(t.Parameters)
				}
				chatTools = append(chatTools, map[string]interface{}{
					"type":     "function",
					"function": fn,
				})
			}
			if len(chatTools) > 0 {
				body["tools"] = chatTools
			}
		}
	}

	if len(req.ToolChoice) > 0 {
		body["tool_choice"] = json.RawMessage(req.ToolChoice)
	}

	if req.MaxOutput > 0 {
		body["max_tokens"] = req.MaxOutput
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}

	return body
}

// translateInputItem converts a Responses API input item to a Chat Completions message.
func translateInputItem(item responsesInputItem) map[string]interface{} {
	if item.Type != "message" {
		return nil
	}

	role := item.Role
	if role == "developer" {
		role = "system"
	}

	// Extract text from content parts.
	var textParts []string
	for _, c := range item.Content {
		if c.Type == "input_text" || c.Type == "output_text" {
			textParts = append(textParts, c.Text)
		}
	}
	content := strings.Join(textParts, "\n")

	return map[string]interface{}{
		"role":    role,
		"content": content,
	}
}

// ---------------------------------------------------------------------------
// Non-stream response translation: Chat Completions → Responses API
// ---------------------------------------------------------------------------

func (s *Server) handleResponsesNonStream(w http.ResponseWriter, r *http.Request, client *joycode.Client, chatBody map[string]interface{}, model string) {
	resp, err := client.Post("/api/saas/openai/v1/chat/completions", chatBody)
	if err != nil {
		slog.Error("responses non-stream upstream error", "model", model, "error", err)
		msg := err.Error()
		code := 500
		if isTimeoutError(msg) {
			code = 504
			msg = "上游服务响应超时，请稍后重试。原始错误: " + msg
		}
		writeError(w, code, msg)
		return
	}

	if usage, ok := resp["usage"].(map[string]interface{}); ok {
		inTk, _ := usage["prompt_tokens"].(float64)
		outTk, _ := usage["completion_tokens"].(float64)
		store.SetTokenUsage(r, int(inTk), int(outTk))
	}

	writeJSON(w, 200, translateChatToResponses(resp, model))
}

func translateChatToResponses(jcResp map[string]interface{}, model string) map[string]interface{} {
	respID := "resp_" + newShortID()
	msgID := "msg_" + newShortID()

	output := make([]interface{}, 0)

	// Extract choices
	choices, _ := jcResp["choices"].([]interface{})
	if len(choices) > 0 {
		choice, _ := choices[0].(map[string]interface{})
		msg, _ := choice["message"].(map[string]interface{})

		// Handle tool calls → function_call output items
		if msg != nil {
			if toolCalls, ok := msg["tool_calls"].([]interface{}); ok {
				for _, tc := range toolCalls {
					tcMap, _ := tc.(map[string]interface{})
					fn, _ := tcMap["function"].(map[string]interface{})
					name, _ := fn["name"].(string)
					args, _ := fn["arguments"].(string)
					callID, _ := tcMap["id"].(string)
					if callID == "" {
						callID = "call_" + newShortID()
					}
					output = append(output, map[string]interface{}{
						"type":   "function_call",
						"id":     callID,
						"call_id": callID,
						"name":   name,
						"arguments": args,
						"status": "completed",
					})
				}
			}

			// Text content → message output item
			text, _ := msg["content"].(string)
			if text != "" || len(output) == 0 {
				contentParts := []interface{}{}
				if text != "" {
					contentParts = append(contentParts, map[string]interface{}{
						"type": "output_text",
						"text": text,
					})
				}
				output = append(output, map[string]interface{}{
					"type":    "message",
					"id":      msgID,
					"status":  "completed",
					"role":    "assistant",
					"content": contentParts,
				})
			}
		}
	}

	// Build usage
	usage := translateUsage(jcResp)

	return map[string]interface{}{
		"id":         respID,
		"object":     "response",
		"created_at": time.Now().Unix(),
		"status":     "completed",
		"model":      model,
		"output":     output,
		"usage":      usage,
	}
}

func translateUsage(jcResp map[string]interface{}) map[string]interface{} {
	inTk, outTk := 0, 0
	if usage, ok := jcResp["usage"].(map[string]interface{}); ok {
		if v, ok := usage["prompt_tokens"].(float64); ok {
			inTk = int(v)
		}
		if v, ok := usage["completion_tokens"].(float64); ok {
			outTk = int(v)
		}
	}
	return map[string]interface{}{
		"input_tokens":  inTk,
		"output_tokens": outTk,
		"total_tokens":  inTk + outTk,
	}
}

// ---------------------------------------------------------------------------
// Stream: Chat Completions SSE → Responses API SSE
// ---------------------------------------------------------------------------

type toolCallState struct {
	id        string
	name      string
	arguments string
	index     int
}

func (s *Server) handleResponsesStream(w http.ResponseWriter, r *http.Request, client *joycode.Client, chatBody map[string]interface{}, model string) {
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

	respID := "resp_" + newShortID()
	msgID := "msg_" + newShortID()

	// Heartbeat goroutine (same pattern as handleStreamChat)
	stopHeartbeat := make(chan struct{})
	heartbeatDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		defer close(heartbeatDone)
		for {
			select {
			case <-stopHeartbeat:
				return
			case <-ticker.C:
				if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}()

	// Send response.created event
	writeSSEEvent(w, "response.created", map[string]interface{}{
		"response": map[string]interface{}{
			"id": respID, "object": "response", "status": "in_progress", "model": model,
		},
	})
	flusher.Flush()

	streamStart := time.Now()
	resp, err := client.PostStream("/api/saas/openai/v1/chat/completions", chatBody)
	if err != nil {
		close(stopHeartbeat)
		<-heartbeatDone
		slog.Error("responses stream upstream error", "model", model, "error", err)
		msg := err.Error()
		if isTimeoutError(msg) {
			msg = "上游服务响应超时，请稍后重试。原始错误: " + msg
		}
		writeSSEEvent(w, "error", map[string]interface{}{"message": msg})
		flusher.Flush()
		writeSSEEvent(w, "response.completed", map[string]interface{}{
			"response": map[string]interface{}{
				"id": respID, "object": "response", "status": "failed", "model": model,
				"output": []interface{}{}, "usage": map[string]int{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0},
			},
		})
		flusher.Flush()
		return
	}
	defer resp.Body.Close()
	close(stopHeartbeat)
	<-heartbeatDone
	slog.Info("stream: connected to upstream", "model", model, "ttfb_ms", time.Since(streamStart).Milliseconds())

	// State machine
	messageStarted := false
	contentPartStarted :=
		false
	var fullText strings.Builder
	toolCalls := map[int]*toolCallState{}
	var inTk, outTk int

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}

		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if chunk.Usage != nil {
			inTk = chunk.Usage.PromptTokens
			outTk = chunk.Usage.CompletionTokens
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]
		delta := choice.Delta

		// --- Text content ---
		if delta.Content != "" {
			if !messageStarted {
				writeSSEEvent(w, "response.output_item.added", map[string]interface{}{
					"output_index": 0,
					"item": map[string]interface{}{
						"type": "message", "id": msgID, "status": "in_progress",
						"role": "assistant", "content": []interface{}{},
					},
				})
				flusher.Flush()
				messageStarted = true
			}
			if !contentPartStarted {
				writeSSEEvent(w, "response.content_part.added", map[string]interface{}{
					"output_index": 0, "content_index": 0,
					"part": map[string]interface{}{"type": "output_text", "text": ""},
				})
				flusher.Flush()
				contentPartStarted = true
			}
			writeSSEEvent(w, "response.output_text.delta", map[string]interface{}{
				"output_index": 0, "content_index": 0, "delta": delta.Content,
			})
			flusher.Flush()
			fullText.WriteString(delta.Content)
		}

		// --- Tool calls ---
		for _, tc := range delta.ToolCalls {
			idx := tc.Index
			state, exists := toolCalls[idx]
			if !exists {
				state = &toolCallState{index: idx}
				toolCalls[idx] = state
			}
			if tc.ID != "" {
				state.id = tc.ID
			}
			if tc.Function.Name != "" {
				state.name = tc.Function.Name
				// Announce the function_call item
				writeSSEEvent(w, "response.output_item.added", map[string]interface{}{
					"output_index": idx + 1,
					"item": map[string]interface{}{
						"type": "function_call", "id": state.id, "status": "in_progress",
						"call_id": state.id, "name": state.name,
					},
				})
				flusher.Flush()
			}
			if tc.Function.Arguments != "" {
				state.arguments += tc.Function.Arguments
				writeSSEEvent(w, "response.function_call_arguments.delta", map[string]interface{}{
					"output_index": idx + 1, "delta": tc.Function.Arguments,
				})
				flusher.Flush()
			}
		}

		// --- Finish ---
		if choice.FinishReason != nil {

			// Close text content part + message item if started
			if contentPartStarted {
				writeSSEEvent(w, "response.output_text.done", map[string]interface{}{
					"output_index": 0, "content_index": 0, "text": fullText.String(),
				})
				flusher.Flush()
				writeSSEEvent(w, "response.content_part.done", map[string]interface{}{
					"output_index": 0, "content_index": 0,
					"part": map[string]interface{}{"type": "output_text", "text": fullText.String()},
				})
				flusher.Flush()
				writeSSEEvent(w, "response.output_item.done", map[string]interface{}{
					"output_index": 0,
					"item": map[string]interface{}{
						"type": "message", "id": msgID, "status": "completed",
						"role": "assistant",
						"content": []interface{}{
							map[string]interface{}{"type": "output_text", "text": fullText.String()},
						},
					},
				})
				flusher.Flush()
			}

			// Close tool call items
			for idx, state := range toolCalls {
				callID := state.id
				if callID == "" {
					callID = "call_" + newShortID()
				}
				writeSSEEvent(w, "response.function_call_arguments.done", map[string]interface{}{
					"output_index": idx + 1, "arguments": state.arguments,
				})
				flusher.Flush()
				writeSSEEvent(w, "response.output_item.done", map[string]interface{}{
					"output_index": idx + 1,
					"item": map[string]interface{}{
						"type": "function_call", "id": callID, "status": "completed",
						"call_id": callID, "name": state.name, "arguments": state.arguments,
					},
				})
				flusher.Flush()
			}

			// Build final output array for response.completed
			finalOutput := buildFinalOutput(msgID, fullText.String(), toolCalls)

			writeSSEEvent(w, "response.completed", map[string]interface{}{
				"response": map[string]interface{}{
					"id":     respID,
					"object": "response",
					"status": "completed",
					"model":  model,
					"output": finalOutput,
					"usage": map[string]interface{}{
						"input_tokens":  inTk,
						"output_tokens": outTk,
						"total_tokens":  inTk + outTk,
					},
				},
			})
			flusher.Flush()
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Error("responses stream read error", "model", model, "error", err)
	}

	// Ensure the stream terminates with [DONE]. Clients like Codex CLI expect
	// this SSE terminator after the response.completed event.
	w.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()

	if inTk > 0 || outTk > 0 {
		store.SetTokenUsage(r, inTk, outTk)
	}
}

// buildFinalOutput constructs the output array for response.completed.
func buildFinalOutput(msgID, text string, toolCalls map[int]*toolCallState) []interface{} {
	output := make([]interface{}, 0)

	// Message item
	contentParts := []interface{}{}
	if text != "" {
		contentParts = append(contentParts, map[string]interface{}{
			"type": "output_text", "text": text,
		})
	}
	output = append(output, map[string]interface{}{
		"type": "message", "id": msgID, "status": "completed",
		"role": "assistant", "content": contentParts,
	})

	// Tool call items (sorted by index)
	for idx := 0; idx < len(toolCalls); idx++ {
		state, ok := toolCalls[idx]
		if !ok {
			continue
		}
		callID := state.id
		if callID == "" {
			callID = "call_" + newShortID()
		}
		output = append(output, map[string]interface{}{
			"type": "function_call", "id": callID, "status": "completed",
			"call_id": callID, "name": state.name, "arguments": state.arguments,
		})
	}

	return output
}

// writeSSEEvent writes a single SSE event with an event type and JSON data.
// The data payload is wrapped to include a "type" field matching the event
// name, which is required by Codex CLI's ResponsesStreamEvent deserializer.
func writeSSEEvent(w http.ResponseWriter, event string, data interface{}) {
	wrapped := map[string]interface{}{"type": event}
	if m, ok := data.(map[string]interface{}); ok {
		for k, v := range m {
			wrapped[k] = v
		}
	}
	jsonData, _ := json.Marshal(wrapped)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, jsonData)
}
