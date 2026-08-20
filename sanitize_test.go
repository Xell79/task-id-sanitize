package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSanitizeArgumentsJSON_StripsUUID(t *testing.T) {
	in := `{"description":"Lint D3 eval files","prompt":"x","subagent_type":"linter","task_id":"e4719854-2005-4b22-b76e-a96077d62aad"}`
	out, ev, ok := sanitizeArgumentsJSON(in)
	if !ok {
		t.Fatal("expected complete JSON")
	}
	if ev == nil {
		t.Fatal("expected strip event")
	}
	if ev.Removed != "e4719854-2005-4b22-b76e-a96077d62aad" {
		t.Fatalf("removed = %q", ev.Removed)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatal(err)
	}
	if _, exists := m["task_id"]; exists {
		t.Fatalf("task_id still present: %s", out)
	}
	if m["subagent_type"] != "linter" {
		t.Fatalf("subagent_type lost: %s", out)
	}
}

func TestSanitizeArgumentsJSON_KeepsSessionID(t *testing.T) {
	in := `{"description":"resume","prompt":"x","subagent_type":"linter","task_id":"ses_testresume0000000000000001"}`
	out, ev, ok := sanitizeArgumentsJSON(in)
	if !ok {
		t.Fatal("expected complete JSON")
	}
	if ev != nil {
		t.Fatalf("unexpected strip: %+v", ev)
	}
	if !strings.Contains(out, "ses_testresume0000000000000001") {
		t.Fatalf("session id lost: %s", out)
	}
}

func TestSanitizeCompleteJSON_ToolCall(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"task","arguments":"{\"description\":\"Lint\",\"prompt\":\"p\",\"subagent_type\":\"linter\",\"task_id\":\"91416d27-10d9-45cc-a680-80ed290fe955\"}"}}]}}]}`)
	out, ev, ok := sanitizeCompleteJSON(body, "json")
	if !ok {
		t.Fatal("expected complete JSON")
	}
	if ev == nil {
		t.Fatal("expected strip event")
	}
	if strings.Contains(string(out), "91416d27-10d9-45cc-a680-80ed290fe955") {
		t.Fatalf("uuid still present: %s", out)
	}
	if !strings.Contains(string(out), `subagent_type`) {
		t.Fatalf("args mangled: %s", out)
	}
}

func TestSSE_HoldUntilCompleteThenStrip(t *testing.T) {
	s := newSSESanitizer()
	nameEvt := []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"task\",\"arguments\":\"\"}}]}}]}\n\n")
	part1 := []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"description\\\":\\\"Lint D3 eval files\\\",\\\"prompt\\\":\\\"x\\\",\\\"subagent_type\\\":\\\"linter\\\",\"}}]}}]}\n\n")
	part2 := []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"task_id\\\":\\\"ed99969c-e722-4310-b127-0a8e2e2641fd\\\"}\"}}]}}]}\n\n")

	out, evs := s.Push(nameEvt)
	if len(out) != 0 {
		t.Fatalf("name chunk should be held, got %d", len(out))
	}
	out, evs = s.Push(part1)
	if len(out) != 0 {
		t.Fatalf("incomplete args should be held, got %d: %s", len(out), out)
	}
	out, evs = s.Push(part2)
	if len(evs) != 1 {
		t.Fatalf("expected 1 strip event, got %d", len(evs))
	}
	if evs[0].Removed != "ed99969c-e722-4310-b127-0a8e2e2641fd" {
		t.Fatalf("removed = %q", evs[0].Removed)
	}
	joined := ""
	for _, c := range out {
		joined += string(c)
	}
	if strings.Contains(joined, "ed99969c-e722-4310-b127-0a8e2e2641fd") {
		t.Fatalf("uuid leaked: %s", joined)
	}
	if !strings.Contains(joined, "subagent_type") {
		t.Fatalf("args lost: %s", joined)
	}
}

func TestSSE_FlushOnDone(t *testing.T) {
	s := newSSESanitizer()
	nameEvt := []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"function\":{\"name\":\"task\",\"arguments\":\"{\\\"description\\\":\\\"Lint\\\",\\\"prompt\\\":\\\"p\\\",\\\"subagent_type\\\":\\\"linter\\\",\\\"task_id\\\":\\\"6f7b67cb-a6e7-4fc3-a7bf-cf47a2a032fb\\\"}\"}}]}}]}\n\n")
	out, evs := s.Push(nameEvt)
	if len(evs) != 1 {
		t.Fatalf("complete args in one event should strip immediately, events=%d out=%d", len(evs), len(out))
	}
	done := []byte("data: [DONE]\n\n")
	out2, evs2 := s.Push(done)
	if len(evs2) != 0 {
		t.Fatalf("done should not re-strip: %+v", evs2)
	}
	if !strings.Contains(string(out2[len(out2)-1]), "[DONE]") {
		t.Fatalf("done missing: %s", out2)
	}
}

func TestStripIncomplete(t *testing.T) {
	in := `{"description":"Lint","prompt":"p","subagent_type":"linter","task_id":"90c4b0e1-ee99-425b-824f-b83f57e99957"`
	out, ev, ok := stripTaskIDFromIncomplete(in)
	if !ok {
		t.Fatal("expected strip of incomplete")
	}
	if ev.Removed != "90c4b0e1-ee99-425b-824f-b83f57e99957" {
		t.Fatalf("removed = %q", ev.Removed)
	}
	if strings.Contains(out, "task_id") {
		t.Fatalf("task_id remains: %s", out)
	}
}

func TestXMLParameter(t *testing.T) {
	in := `<invoke name="task"><parameter name="description">Lint</parameter><parameter name="task_id">c98a830a-d08d-455b-a184-78e1b27c9849</parameter></invoke>`
	out, ev, ok := stripXMLTaskID(in, "xml")
	if !ok || ev == nil {
		t.Fatalf("expected xml strip ok=%v ev=%v", ok, ev)
	}
	if strings.Contains(out, "task_id") {
		t.Fatalf("xml task_id remains: %s", out)
	}
}
