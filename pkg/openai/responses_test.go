package openai

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestTranslateResponsesToChat_BasicMessages(t *testing.T) {
	req := &ResponsesRequest{
		Model:        "GLM-5.2",
		Instructions: "You are helpful.",
		Input: json.RawMessage(`[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi there"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"bye"}]}
		]`),
		Stream: false,
	}

	body := translateResponsesToChat(req)

	messages, ok := body["messages"].([]interface{})
	if !ok {
		t.Fatal("messages should be a slice")
	}

	// instructions → system message + 3 input items = 4 total
	if len(messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(messages))
	}

	// First message should be system from instructions
	first, _ := messages[0].(map[string]interface{})
	if first["role"] != "system" {
		t.Errorf("expected system role, got %v", first["role"])
	}
	if first["content"] != "You are helpful." {
		t.Errorf("expected instructions as content, got %v", first["content"])
	}

	// Second should be user "hello"
	second, _ := messages[1].(map[string]interface{})
	if second["role"] != "user" {
		t.Errorf("expected user role, got %v", second["role"])
	}
	if second["content"] != "hello" {
		t.Errorf("expected 'hello', got %v", second["content"])
	}

	// Third should be assistant "hi there"
	third, _ := messages[2].(map[string]interface{})
	if third["role"] != "assistant" {
		t.Errorf("expected assistant role, got %v", third["role"])
	}
	if third["content"] != "hi there" {
		t.Errorf("expected 'hi there', got %v", third["content"])
	}
}

func TestTranslateResponsesToChat_DeveloperRole(t *testing.T) {
	req := &ResponsesRequest{
		Model: "GLM-5.2",
		Input: json.RawMessage(`[
			{"type":"message","role":"developer","content":[{"type":"input_text","text":"system instructions"}]}
		]`),
	}

	body := translateResponsesToChat(req)
	messages := body["messages"].([]interface{})

	msg, _ := messages[0].(map[string]interface{})
	if msg["role"] != "system" {
		t.Errorf("developer role should map to system, got %v", msg["role"])
	}
}

func TestTranslateResponsesToChat_MultiPartContent(t *testing.T) {
	req := &ResponsesRequest{
		Model: "GLM-5.2",
		Input: json.RawMessage(`[
			{"type":"message","role":"user","content":[
				{"type":"input_text","text":"part1"},
				{"type":"input_text","text":"part2"}
			]}
		]`),
	}

	body := translateResponsesToChat(req)
	messages := body["messages"].([]interface{})

	msg, _ := messages[0].(map[string]interface{})
	content, _ := msg["content"].(string)
	if !strings.Contains(content, "part1") || !strings.Contains(content, "part2") {
		t.Errorf("expected content to contain both parts, got %v", content)
	}
}

func TestTranslateResponsesToChat_Tools(t *testing.T) {
	req := &ResponsesRequest{
		Model: "GLM-5.2",
		Input: json.RawMessage(`[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]`),
		Tools: json.RawMessage(`[
			{"type":"function","name":"calc","description":"calculator","parameters":{"type":"object","properties":{"x":{"type":"string"}}}},
			{"type":"web_search"},
			{"type":"function","name":"shell","parameters":{"type":"object"}}
		]`),
	}

	body := translateResponsesToChat(req)
	tools, ok := body["tools"].([]interface{})
	if !ok {
		t.Fatal("tools should be present")
	}

	// Only function-type tools should pass through (web_search filtered out)
	if len(tools) != 2 {
		t.Fatalf("expected 2 function tools, got %d", len(tools))
	}

	first, _ := tools[0].(map[string]interface{})
	if first["type"] != "function" {
		t.Errorf("expected type=function, got %v", first["type"])
	}
	fn, _ := first["function"].(map[string]interface{})
	if fn["name"] != "calc" {
		t.Errorf("expected name=calc, got %v", fn["name"])
	}
	if fn["description"] != "calculator" {
		t.Errorf("expected description=calculator, got %v", fn["description"])
	}
}

func TestTranslateResponsesToChat_OptionalFields(t *testing.T) {
	temp := 0.7
	topP := 0.9
	req := &ResponsesRequest{
		Model:       "GLM-5.2",
		Input:       json.RawMessage(`[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]`),
		Stream:      true,
		MaxOutput:   4096,
		Temperature: &temp,
		TopP:        &topP,
		ToolChoice:  json.RawMessage(`"auto"`),
	}

	body := translateResponsesToChat(req)

	if body["stream"] != true {
		t.Errorf("expected stream=true, got %v", body["stream"])
	}
	if body["max_tokens"] != 4096 {
		t.Errorf("expected max_tokens=4096, got %v", body["max_tokens"])
	}
	if body["temperature"] != 0.7 {
		t.Errorf("expected temperature=0.7, got %v", body["temperature"])
	}
	if body["top_p"] != 0.9 {
		t.Errorf("expected top_p=0.9, got %v", body["top_p"])
	}
	if _, ok := body["tool_choice"]; !ok {
		t.Error("tool_choice should be present")
	}
}

func TestTranslateChatToResponses_TextOnly(t *testing.T) {
	jcResp := map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "Hello world",
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     float64(10),
			"completion_tokens":  float64(2),
		},
	}

	result := translateChatToResponses(jcResp, "GLM-5.2")

	if result["object"] != "response" {
		t.Errorf("expected object=response, got %v", result["object"])
	}
	if result["status"] != "completed" {
		t.Errorf("expected status=completed, got %v", result["status"])
	}

	output, _ := result["output"].([]interface{})
	if len(output) != 1 {
		t.Fatalf("expected 1 output item, got %d", len(output))
	}

	msg, _ := output[0].(map[string]interface{})
	if msg["type"] != "message" {
		t.Errorf("expected type=message, got %v", msg["type"])
	}

	content, _ := msg["content"].([]interface{})
	if len(content) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(content))
	}
	part, _ := content[0].(map[string]interface{})
	if part["type"] != "output_text" {
		t.Errorf("expected output_text, got %v", part["type"])
	}
	if part["text"] != "Hello world" {
		t.Errorf("expected 'Hello world', got %v", part["text"])
	}

	usage, _ := result["usage"].(map[string]interface{})
	if usage["input_tokens"] != 10 {
		t.Errorf("expected input_tokens=10, got %v", usage["input_tokens"])
	}
	if usage["output_tokens"] != 2 {
		t.Errorf("expected output_tokens=2, got %v", usage["output_tokens"])
	}
}

func TestTranslateChatToResponses_ToolCalls(t *testing.T) {
	jcResp := map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "",
					"tool_calls": []interface{}{
						map[string]interface{}{
							"id": "call_abc",
							"function": map[string]interface{}{
								"name":      "calculator",
								"arguments": `{"x":"1+1"}`,
							},
						},
					},
				},
				"finish_reason": "tool_calls",
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     float64(20),
			"completion_tokens":  float64(5),
		},
	}

	result := translateChatToResponses(jcResp, "GLM-5.2")

	output, _ := result["output"].([]interface{})
	// Should have 1 function_call item (empty text → message item with empty content)
	if len(output) < 1 {
		t.Fatalf("expected at least 1 output item, got %d", len(output))
	}

	// Find the function_call item
	var fcItem map[string]interface{}
	for _, item := range output {
		m, _ := item.(map[string]interface{})
		if m["type"] == "function_call" {
			fcItem = m
			break
		}
	}
	if fcItem == nil {
		t.Fatal("expected a function_call output item")
	}
	if fcItem["name"] != "calculator" {
		t.Errorf("expected name=calculator, got %v", fcItem["name"])
	}
	if fcItem["arguments"] != `{"x":"1+1"}` {
		t.Errorf("expected arguments, got %v", fcItem["arguments"])
	}
	if fcItem["id"] != "call_abc" {
		t.Errorf("expected id=call_abc, got %v", fcItem["id"])
	}
}

func TestTranslateChatToResponses_EmptyChoices(t *testing.T) {
	jcResp := map[string]interface{}{
		"choices": []interface{}{},
	}

	result := translateChatToResponses(jcResp, "GLM-5.2")

	// Empty choices → no message content, but still returns a valid response object
	if result["object"] != "response" {
		t.Errorf("expected object=response, got %v", result["object"])
	}
	if result["status"] != "completed" {
		t.Errorf("expected status=completed, got %v", result["status"])
	}

	output, _ := result["output"].([]interface{})
	// With empty choices, output should be empty (no message item since text=="" and no tool_calls)
	if len(output) != 0 {
		t.Fatalf("expected 0 output items for empty choices, got %d", len(output))
	}
}

func TestWriteSSEEvent(t *testing.T) {
	var buf strings.Builder
	w := &mockResponseWriter{buf: &buf}

	writeSSEEvent(w, "response.created", map[string]interface{}{
		"id": "resp_123", "status": "in_progress",
	})

	output := buf.String()
	if !strings.HasPrefix(output, "event: response.created\n") {
		t.Errorf("expected event line, got: %s", output)
	}
	if !strings.Contains(output, "data: ") {
		t.Errorf("expected data line, got: %s", output)
	}
	if !strings.Contains(output, "resp_123") {
		t.Errorf("expected data to contain id, got: %s", output)
	}
	if !strings.HasSuffix(output, "\n\n") {
		t.Errorf("expected double newline terminator, got: %s", output)
	}
}

// mockResponseWriter implements http.ResponseWriter for testing.
type mockResponseWriter struct {
	buf    *strings.Builder
	header map[string][]string
}

func (m *mockResponseWriter) Header() http.Header {
	if m.header == nil {
		m.header = map[string][]string{}
	}
	return m.header
}

func (m *mockResponseWriter) Write(b []byte) (int, error) {
	return m.buf.Write(b)
}

func (m *mockResponseWriter) WriteHeader(code int) {}
