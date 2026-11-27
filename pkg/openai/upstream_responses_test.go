package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// nativeTestServer wires a Server whose client points at a mock upstream.
func nativeTestServer(upstream http.HandlerFunc) (*Server, *httptest.Server) {
	backend := httptest.NewServer(upstream)
	client := newMockClient(backend)
	srv := NewServer(client, nil)
	return srv, backend
}

func newNativeRequest() *http.Request {
	r, _ := http.NewRequestWithContext(context.Background(), "POST", "/v1/responses", nil)
	return r
}

// sseLines wraps each line in the gateway's nested SSE envelope.
func sseLines(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("data: data: ")
		b.WriteString(l)
		b.WriteString("\n\n")
	}
	return b.String()
}

// Empty upstream stream (connects, produces nothing, closes) must surface as
// response.failed, never as a fake zero-usage completion.
func TestNativeResponsesStream_EmptyUpstream_EmitsFailed(t *testing.T) {
	srv, backend := nativeTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
	})
	defer backend.Close()

	rec := httptest.NewRecorder()
	body := map[string]interface{}{"model": "GPT-6 Astra", "stream": true,
		"input": []interface{}{map[string]interface{}{"type": "message", "role": "user"}}}
	srv.handleResponsesNativeStream(rec, newNativeRequest(), srv.Client, body, "GPT-6 Astra")

	out := rec.Body.String()
	if !strings.Contains(out, `"type":"response.failed"`) {
		t.Errorf("expected response.failed, got:\n%s", out)
	}
	if !strings.Contains(out, "上游连接中断") {
		t.Errorf("expected interrupt message in failed event, got:\n%s", out)
	}
	if strings.Contains(out, `"status":"completed"`) {
		t.Errorf("must not synthesize a completed response:\n%s", out)
	}
}

// A stream that delivers partial text then drops must keep the partial output
// and synthesize a completed response.
func TestNativeResponsesStream_PartialOutput_SynthesizesCompleted(t *testing.T) {
	srv, backend := nativeTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		io.WriteString(w, sseLines(
			`{"type":"response.output_text.delta","delta":"部分输出"}`,
		))
	})
	defer backend.Close()

	rec := httptest.NewRecorder()
	body := map[string]interface{}{"model": "GPT-6 Astra", "stream": true}
	srv.handleResponsesNativeStream(rec, newNativeRequest(), srv.Client, body, "GPT-6 Astra")

	out := rec.Body.String()
	if strings.Contains(out, `"type":"response.failed"`) {
		t.Errorf("partial output should complete, not fail:\n%s", out)
	}
	if !strings.Contains(out, `"type":"response.completed"`) {
		t.Errorf("expected synthesized response.completed:\n%s", out)
	}
	if !strings.Contains(out, "部分输出") {
		t.Errorf("synthesized response should keep partial text:\n%s", out)
	}
}

// An upstream error payload mid-stream must surface as response.failed with
// the upstream message.
func TestNativeResponsesStream_UpstreamErrorPayload_EmitsFailed(t *testing.T) {
	srv, backend := nativeTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		io.WriteString(w, sseLines(
			`{"error":{"code":"6001","message":"当前模型不可用或已下线"}}`,
		))
	})
	defer backend.Close()

	rec := httptest.NewRecorder()
	body := map[string]interface{}{"model": "GPT-6 Astra", "stream": true}
	srv.handleResponsesNativeStream(rec, newNativeRequest(), srv.Client, body, "GPT-6 Astra")

	out := rec.Body.String()
	if !strings.Contains(out, `"type":"response.failed"`) {
		t.Errorf("expected response.failed, got:\n%s", out)
	}
	if !strings.Contains(out, "当前模型不可用或已下线") {
		t.Errorf("expected upstream error message in failed event:\n%s", out)
	}
}

// The reasoning field from a Responses request must be forwarded upstream so
// Codex's model_reasoning_effort takes effect.
func TestResponsesNative_ForwardsReasoning(t *testing.T) {
	var upstreamBody map[string]interface{}
	srv, backend := nativeTestServer(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &upstreamBody)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		io.WriteString(w, sseLines(
			`{"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1}}}`,
		))
	})
	defer backend.Close()

	req := &ResponsesRequest{
		Model:     "GPT-6 Astra",
		Stream:    true,
		Reasoning: json.RawMessage(`{"effort":"high"}`),
	}
	rec := httptest.NewRecorder()
	srv.handleResponsesNative(rec, newNativeRequest(), srv.Client, req, "GPT-6 Astra")

	if upstreamBody["reasoning"] == nil {
		t.Fatalf("reasoning not forwarded upstream, body: %v", upstreamBody)
	}
	if !bytes.Contains([]byte(rec.Body.String()), []byte(`"type":"response.completed"`)) {
		t.Errorf("expected forwarded response.completed, got:\n%s", rec.Body.String())
	}
}

// An Azure resource-affinity rejection (history items created under another
// Azure OpenAI resource) must be retried once with the input item ids
// stripped, so long-running conversations keep working instead of failing
// every retry.
func TestNativeResponsesStream_AzureResourceError_RetriesWithoutIDs(t *testing.T) {
	var requests int
	var retryBody map[string]interface{}
	srv, backend := nativeTestServer(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		if requests == 1 {
			io.WriteString(w, sseLines(
				`{"error":{"cause":"The requested item was created under a different Azure OpenAI resource. Use the same resource that created the item to access it.","code":400,"message":"模型服务调用失败","status":"FAILED_RESPONSE"}`,
			))
			return
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &retryBody)
		io.WriteString(w, sseLines(
			`{"type":"response.output_text.delta","delta":"ok"}`,
			`{"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1}}}`,
		))
	})
	defer backend.Close()

	rec := httptest.NewRecorder()
	body := map[string]interface{}{"model": "GPT-6 Astra", "stream": true,
		"input": []interface{}{
			map[string]interface{}{"type": "reasoning", "id": "rs_1", "encrypted_content": "blob"},
			map[string]interface{}{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "f", "arguments": "{}"},
			map[string]interface{}{"type": "function_call_output", "id": "fco_1", "call_id": "call_1", "output": "out"},
		}}
	srv.handleResponsesNativeStream(rec, newNativeRequest(), srv.Client, body, "GPT-6 Astra")

	if requests != 2 {
		t.Fatalf("expected 2 upstream attempts, got %d", requests)
	}
	input, _ := retryBody["input"].([]interface{})
	if len(input) != 3 {
		t.Fatalf("expected 3 input items in retry body, got %d", len(input))
	}
	for i, it := range input {
		m, _ := it.(map[string]interface{})
		if m["type"] == "function_call_output" {
			if m["id"] != "fco_1" {
				t.Errorf("function_call_output id must be kept, got %v", m["id"])
			}
			continue
		}
		if _, has := m["id"]; has {
			t.Errorf("input item %d (type=%v) should have id stripped: %v", i, m["type"], m["id"])
		}
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"type":"response.completed"`) {
		t.Errorf("expected response.completed after retry, got:\n%s", out)
	}
}

// The affinity failure sometimes surfaces as a bare empty stream instead of
// the error payload; that must also be retried once, and still-empty after
// the retry surfaces response.failed as before.
func TestNativeResponsesStream_EmptyUpstream_RetriesOnceThenFails(t *testing.T) {
	var requests int
	srv, backend := nativeTestServer(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
	})
	defer backend.Close()

	rec := httptest.NewRecorder()
	body := map[string]interface{}{"model": "GPT-6 Astra", "stream": true,
		"input": []interface{}{map[string]interface{}{"type": "message", "role": "user", "id": "msg_1"}}}
	srv.handleResponsesNativeStream(rec, newNativeRequest(), srv.Client, body, "GPT-6 Astra")

	if requests != 2 {
		t.Errorf("expected 2 upstream attempts (one retry), got %d", requests)
	}
	if !strings.Contains(rec.Body.String(), `"type":"response.failed"`) {
		t.Errorf("expected response.failed after retry exhaustion, got:\n%s", rec.Body.String())
	}
}
