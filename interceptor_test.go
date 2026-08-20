package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func decodeIntercept(t *testing.T, raw []byte) interceptResp {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("envelope: %v body=%s", err, raw)
	}
	if !env.OK {
		t.Fatalf("envelope not ok: %s", raw)
	}
	var resp interceptResp
	if len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, &resp); err != nil {
			t.Fatalf("result: %v body=%s", err, env.Result)
		}
	}
	return resp
}

func TestInterceptJSON_StripsUUIDToolCall(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"task","arguments":"{\"description\":\"Lint\",\"prompt\":\"p\",\"subagent_type\":\"linter\",\"task_id\":\"91416d27-10d9-45cc-a680-80ed290fe955\"}"}}]}}]}`)
	raw, _ := json.Marshal(map[string]any{"Body": body, "Model": "grok-4.6"})
	out, err := interceptJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeIntercept(t, out)
	if resp.DropChunk {
		t.Fatal("DropChunk must stay false on JSON intercept")
	}
	if len(resp.Body) == 0 {
		t.Fatal("expected rewritten body")
	}
	if strings.Contains(string(resp.Body), "91416d27-10d9-45cc-a680-80ed290fe955") {
		t.Fatalf("uuid leaked: %s", resp.Body)
	}
	if strings.Contains(string(resp.Body), `"task_id"`) {
		t.Fatalf("task_id remains: %s", resp.Body)
	}
}

func TestInterceptJSON_KeepsSessionTaskID(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"task","arguments":"{\"subagent_type\":\"linter\",\"task_id\":\"ses_testresume0000000000000001\"}"}}]}}]}`)
	raw, _ := json.Marshal(map[string]any{"Body": body})
	out, err := interceptJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeIntercept(t, out)
	if len(resp.Body) != 0 {
		t.Fatalf("session id should pass through unchanged, got %s", resp.Body)
	}
}

func TestInterceptStream_HeaderInitIsNoop(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"ChunkIndex": -1, "Body": []byte("ignored")})
	out, err := interceptStream(raw)
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeIntercept(t, out)
	if resp.DropChunk || len(resp.Body) != 0 {
		t.Fatalf("header-init must be noop: %+v", resp)
	}
}

func TestInterceptStream_FirstRoleChunkNeverDropped(t *testing.T) {
	chunk := []byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")
	raw, _ := json.Marshal(map[string]any{"Body": chunk, "ChunkIndex": 0, "Model": "grok-4.6"})
	out, err := interceptStream(raw)
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeIntercept(t, out)
	if resp.DropChunk {
		t.Fatal("DropChunk on first payload causes empty_stream")
	}
}

func TestInterceptStream_StripsUUIDDeltaWithoutDrop(t *testing.T) {
	chunk := []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"task\",\"arguments\":\"{\\\"task_id\\\":\\\"ed99969c-e722-4310-b127-0a8e2e2641fd\\\"}\"}}]}}]}\n\n")
	raw, _ := json.Marshal(map[string]any{"Body": chunk, "ChunkIndex": 1, "Model": "grok-4.6"})
	out, err := interceptStream(raw)
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeIntercept(t, out)
	if resp.DropChunk {
		t.Fatal("must not drop rewritten chunk")
	}
	if len(resp.Body) == 0 {
		t.Fatal("expected rewritten SSE body")
	}
	if strings.Contains(string(resp.Body), "ed99969c-e722-4310-b127-0a8e2e2641fd") {
		t.Fatalf("uuid leaked: %s", resp.Body)
	}
}
