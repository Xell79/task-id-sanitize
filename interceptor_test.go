package main

import (
	"encoding/json"
	"os"
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

func TestConfigureRecordsVersionAndLogsUpgrade(t *testing.T) {
	dir := t.TempDir()
	logMu.Lock()
	oldLog := logPath
	logPath = dir + "/plugin.log"
	logMu.Unlock()
	t.Cleanup(func() {
		logMu.Lock()
		logPath = oldLog
		logMu.Unlock()
	})
	stateMu.Lock()
	lastVersion = "0.1.1"
	stateMu.Unlock()

	configure(nil, "plugin.reconfigure")

	stateMu.Lock()
	got := lastVersion
	stateMu.Unlock()
	if got != pluginVersion {
		t.Fatalf("lastVersion=%q want %q", got, pluginVersion)
	}
	data, err := os.ReadFile(dir + "/plugin.log")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"event":"plugin.configure"`) {
		t.Fatalf("expected plugin.configure log entry, got:\n%s", data)
	}
	if !strings.Contains(string(data), `"event":"plugin.upgrade"`) {
		t.Fatalf("expected plugin.upgrade log entry, got:\n%s", data)
	}
	if !strings.Contains(string(data), `"event":"plugin.reset"`) {
		t.Fatalf("expected plugin.reset log entry, got:\n%s", data)
	}
	// SEC-5: the log contains removed task_id values; it must not be
	// world/group readable.
	info, err := os.Stat(dir + "/plugin.log")
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("log mode = %o, want 600", perm)
	}
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

func TestInterceptStream_FirstKeyIncomplete(t *testing.T) {
	// CR-1 end to end: task_id is the first key of an arguments
	// fragment cut mid-object. The rewritten arguments must have no
	// dangling comma before the remaining member.
	chunk := []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"task\",\"arguments\":\"{\\\"task_id\\\":\\\"91416d27-10d9-45cc-a680-80ed290fe955\\\",\\\"subagent_type\\\":\\\"linter\\\"\"}}]}}]}\n\n")
	raw, _ := json.Marshal(map[string]any{"Body": chunk, "ChunkIndex": 2, "Model": "grok-4.6"})
	out, err := interceptStream(raw)
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeIntercept(t, out)
	if resp.DropChunk || len(resp.Body) == 0 {
		t.Fatalf("expected rewritten body, got %+v", resp)
	}
	var parsed struct {
		Choices []struct {
			Delta struct {
				ToolCalls []struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	line := strings.SplitN(string(resp.Body), "\n", 2)[0]
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &parsed); err != nil {
		t.Fatalf("rewritten event invalid: %v\n%s", err, resp.Body)
	}
	args := parsed.Choices[0].Delta.ToolCalls[0].Function.Arguments
	if args != `{"subagent_type":"linter"` {
		t.Fatalf("arguments = %q, want {\"subagent_type\":\"linter\"", args)
	}
}

func TestInterceptStream_DuplicateTaskIDKeys(t *testing.T) {
	// Review-2 regression: duplicate task_id keys, the first one in
	// first-key position, followed by another member. Used to panic
	// (reversed slice bounds) or emit `{,"x":1`.
	chunk := []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"task\",\"arguments\":\"{\\\"task_id\\\":\\\"a_UUID_a\\\",\\\"task_id\\\":\\\"b_UUID_b\\\",\\\"x\\\":1\"}}]}}]}\n\n")
	raw, _ := json.Marshal(map[string]any{"Body": chunk, "ChunkIndex": 3, "Model": "grok-4.6"})
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
	s := string(resp.Body)
	if strings.Contains(s, "task_id") || strings.Contains(s, "a_UUID_a") || strings.Contains(s, "b_UUID_b") {
		t.Fatalf("task_id leaked: %s", s)
	}
	var parsed struct {
		Choices []struct {
			Delta struct {
				ToolCalls []struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	line := strings.SplitN(s, "\n", 2)[0]
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &parsed); err != nil {
		t.Fatalf("rewritten event invalid: %v\n%s", err, s)
	}
	if args := parsed.Choices[0].Delta.ToolCalls[0].Function.Arguments; args != `{"x":1` {
		t.Fatalf("arguments = %q, want {\"x\":1", args)
	}
}

func TestInterceptJSON_MalformedRequestIsNoop(t *testing.T) {
	out, err := interceptJSON([]byte(`{not json`))
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeIntercept(t, out)
	if resp.DropChunk || len(resp.Body) != 0 {
		t.Fatalf("malformed request must be a no-op: %+v", resp)
	}
}

func TestHandleMethod_UnknownMethod(t *testing.T) {
	raw, err := handleMethod("no.such.method", nil)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.OK || env.Error == nil || env.Error.Code != "unknown_method" {
		t.Fatalf("envelope = %+v", env)
	}
}

func TestHandleMethod_InterceptorFlow(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"task","arguments":"{\"task_id\":\"91416d27-10d9-45cc-a680-80ed290fe955\"}"}}]}}]}`)
	req, _ := json.Marshal(map[string]any{"Body": body, "Model": "grok-4.6"})
	raw, err := handleMethod("response.intercept_after", req)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("envelope not ok: %s", raw)
	}
	var resp interceptResp
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(resp.Body), "91416d27") {
		t.Fatalf("uuid leaked: %s", resp.Body)
	}
}
