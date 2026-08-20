package main

import (
	"bytes"
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

func TestSSE_PassThroughThenStripUUIDFragment(t *testing.T) {
	s := newSSESanitizer()
	nameEvt := []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"task\",\"arguments\":\"\"}}]}}]}\n\n")
	part1 := []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"description\\\":\\\"Lint D3 eval files\\\",\\\"prompt\\\":\\\"x\\\",\\\"subagent_type\\\":\\\"linter\\\",\"}}]}}]}\n\n")
	part2 := []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"task_id\\\":\\\"ed99969c-e722-4310-b127-0a8e2e2641fd\\\"}\"}}]}}]}\n\n")

	out, evs := s.Push(nameEvt)
	if len(out) == 0 {
		t.Fatal("name chunk must be forwarded (no DropChunk / empty_stream)")
	}
	if len(evs) != 0 {
		t.Fatalf("name chunk should not strip, events=%d", len(evs))
	}
	out, evs = s.Push(part1)
	if len(out) == 0 {
		t.Fatal("incomplete args without task_id must be forwarded")
	}
	out, evs = s.Push(part2)
	if len(evs) != 1 {
		t.Fatalf("expected 1 strip event, got %d out=%s", len(evs), out)
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
	if strings.Contains(joined, "task_id") {
		t.Fatalf("task_id remains: %s", joined)
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

func TestStripIncomplete_CommaPlacement(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"first key", `{"task_id":"abc-123","subagent_type":"linter"`, `{"subagent_type":"linter"`},
		{"first key unquoted", `{"task_id":123,"b":2`, `{"b":2`},
		{"first key no trailing", `{"task_id":"abc-123"`, `{`},
		{"middle key", `{"a":1,"task_id":"abc-123","b":2`, `{"a":1,"b":2`},
		{"last key", `{"a":1,"task_id":"abc-123"`, `{"a":1`},
		// Whitespace around the swallowed comma is preserved as-is;
		// `{  "b":2` is valid JSON once the fragment completes.
		{"spaced first key", `{ "task_id":"abc-123" , "b":2`, `{  "b":2`},
		{"spaced first key unquoted", `{"task_id" : 123 , "b":2`, `{ "b":2`},

		// Duplicate task_id keys: removing the first-key member
		// promotes the duplicate to first-key; its trailing comma is
		// swallowed too (chain). Used to panic with reversed slice
		// bounds and to emit `{,"x":3`.
		{"dup keys first, no trailing member", `{"task_id":1,"task_id":2`, `{`},
		{"dup keys quoted, no trailing", `{"task_id":"a","task_id":"b"`, `{`},
		{"dup keys with trailing member", `{"task_id":"a","task_id":"b","x":1`, `{"x":1`},
		{"dup keys unquoted with trailing", `{"task_id":1,"task_id":2,"x":3`, `{"x":3`},
		{"dup keys spaced", `{"task_id":1 , "task_id":2 , "x":3`, `{ "x":3`},
		{"dup keys in the middle", `{"a":1,"task_id":2,"task_id":3,"x":4`, `{"a":1,"x":4`},
		{"first-key then other member then task_id", `{"task_id":1,"x":2,"task_id":3,"y":4`, `{"x":2,"y":4`},

		// Nested objects and arrays: first-key is scoped to the
		// nearest opening brace, not the fragment start alone.
		{"nested first key", `{"a":{"task_id":1,"b":2`, `{"a":{"b":2`},
		{"nested inner complete", `{"a":{"task_id":1},"b":2`, `{"a":{},"b":2`},
		{"array element first key", `[{"task_id":1,"b":2`, `[{"b":2`},

		// Bare fragments (non-cumulative SSE deltas never include
		// the opening brace): treated as first-key, so the trailing
		// comma is swallowed to avoid a double comma when the
		// previous delta already ended with one.
		{"bare start with trailing member", `"task_id":"u","subagent_type":"linter"`, `"subagent_type":"linter"`},
		{"bare start trailing comma only", `"task_id":"u",`, ``},
		{"bare start no comma", `"task_id":"u"`, ``},
		// The regex consumes the member's leading comma, so a bare
		// fragment ending in task_id loses the separator entirely —
		// the next delta brings its own comma.
		{"bare middle fragment", `"a":1,"task_id":"u"`, `"a":1`},

		// Value shapes and case-insensitive key matching.
		{"escaped quote in value", `{"task_id":"a\"b","x":1`, `{"x":1`},
		{"case-insensitive key", `{"Task_ID":1,"b":2`, `{"b":2`},
		{"task_id inside string value", `{"a":"task_id:1","b":2`, `{"a":"task_id:1","b":2`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, ev, ok := stripTaskIDFromIncomplete(tc.in)
			if tc.in == tc.want {
				// Expect passthrough (no change).
				if ok || ev != nil {
					t.Fatalf("expected passthrough, got ok=%v out=%q", ok, out)
				}
				return
			}
			if !ok || ev == nil {
				t.Fatalf("expected strip, ok=%v ev=%v", ok, ev)
			}
			if out != tc.want {
				t.Fatalf("out = %q, want %q", out, tc.want)
			}
		})
	}
}

func TestStripIncomplete_SessionIDPassthrough(t *testing.T) {
	in := `{"task_id":"ses_resume00000000000000001","x":1`
	out, ev, ok := stripTaskIDFromIncomplete(in)
	if ok || ev != nil {
		t.Fatalf("session id must pass through, ok=%v ev=%v", ok, ev)
	}
	if out != in {
		t.Fatalf("out = %q, want unchanged", out)
	}
}

func TestStripIncomplete_NoTaskID(t *testing.T) {
	out, ev, ok := stripTaskIDFromIncomplete(`{"a":1,"b":2`)
	if ok || ev != nil || out != `{"a":1,"b":2` {
		t.Fatalf("unexpected strip: ok=%v ev=%v out=%q", ok, ev, out)
	}
}

func TestShouldStripTaskID_NonStringValues(t *testing.T) {
	if removed, strip := shouldStripTaskID(nil); !strip || removed != "null" {
		t.Fatalf("nil: removed=%q strip=%v (want \"null\", true)", removed, strip)
	}
	if removed, strip := shouldStripTaskID(42); !strip || removed != "42" {
		t.Fatalf("number: removed=%q strip=%v (want \"42\", true)", removed, strip)
	}
	if removed, strip := shouldStripTaskID("  ses_abc  "); strip || removed != "ses_abc" {
		t.Fatalf("ses id: removed=%q strip=%v (want keep)", removed, strip)
	}
}

func TestSanitizeCompleteJSON_TaskShapeWithoutName(t *testing.T) {
	// description+prompt+subagent_type shape with a task_id but no
	// tool name: stripped via hasTaskShape.
	body := []byte(`{"tool_calls":[{"description":"d","prompt":"p","subagent_type":"linter","task_id":"e4719854-2005-4b22-b76e-a96077d62aad"}]}`)
	out, ev, ok := sanitizeCompleteJSON(body, "json")
	if !ok || ev == nil {
		t.Fatalf("expected strip via task shape, ok=%v ev=%v", ok, ev)
	}
	s := string(out)
	if strings.Contains(s, "e4719854") || strings.Contains(s, "task_id") {
		t.Fatalf("task_id leaked: %s", s)
	}
	if !json.Valid(out) {
		t.Fatalf("output not valid JSON: %s", s)
	}
}

func TestSanitizeCompleteJSON_NoHTMLEscaping(t *testing.T) {
	// Re-marshaling must not HTML-escape "<" to \u003c: the XML pass
	// runs after the JSON rewrite and must still see literal XML.
	body := []byte(`{"o":{"description":"d","prompt":"p","subagent_type":"l","task_id":"e4719854-2005-4b22-b76e-a96077d62aad"},"keep":"<b>text</b>"}`)
	out, _, ok := sanitizeCompleteJSON(body, "json")
	if !ok {
		t.Fatal("expected complete JSON")
	}
	s := string(out)
	if !strings.Contains(s, `"<b>text</b>"`) {
		t.Fatalf("HTML escaping regressed (or keep lost): %s", s)
	}
	if strings.Contains(s, `u003c`) {
		t.Fatalf("output contains \\u003c escaping: %s", s)
	}
	if !json.Valid(out) {
		t.Fatalf("output not valid JSON: %s", s)
	}
}

func TestStripXMLTaskID_EscapedQuotes(t *testing.T) {
	// XML embedded inside a JSON string value carries escaped quotes:
	// name=\"task_id\". The regex must match both raw and escaped.
	raw := `<parameter name="task_id">c98a830a-d08d-455b-a184-78e1b27c9849</parameter>`
	out, ev, ok := stripXMLTaskID(raw, "xml")
	if !ok || ev == nil {
		t.Fatalf("raw xml: ok=%v ev=%v", ok, ev)
	}
	if strings.Contains(out, "c98a830a") {
		t.Fatalf("raw xml leaked: %s", out)
	}

	esc := `<parameter name=\"task_id\">c98a830a-d08d-455b-a184-78e1b27c9849</parameter>`
	out, ev, ok = stripXMLTaskID(esc, "xml")
	if !ok || ev == nil {
		t.Fatalf("escaped xml: ok=%v ev=%v", ok, ev)
	}
	if strings.Contains(out, "c98a830a") {
		t.Fatalf("escaped xml leaked: %s", out)
	}
}

func TestSplitSSE_CRLF(t *testing.T) {
	buf := []byte("data: a\r\n\r\ndata: b\r\n\r\n")
	var events [][]byte
	rest := buf
	for {
		ev, next, ok := splitSSE(rest)
		if !ok {
			break
		}
		events = append(events, ev)
		rest = next
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if string(sseDataPayload(events[0])) != "a" {
		t.Fatalf("payload[0] = %q", sseDataPayload(events[0]))
	}
}

func TestSSE_PassthroughWithoutDataLines(t *testing.T) {
	s := newSSESanitizer()
	chunk := []byte(": keepalive\n\n")
	out, evs := s.Push(chunk)
	if len(evs) != 0 {
		t.Fatalf("comment event must not strip: %+v", evs)
	}
	joined := string(bytes.Join(out, nil))
	if joined != string(chunk) {
		t.Fatalf("keepalive mangled: %q", joined)
	}
}

func TestSSE_InvalidJSONPayloadPassthrough(t *testing.T) {
	s := newSSESanitizer()
	chunk := []byte("data: {not json\n\n")
	out, evs := s.Push(chunk)
	if len(evs) != 0 {
		t.Fatalf("invalid json must not strip: %+v", evs)
	}
	if string(bytes.Join(out, nil)) != string(chunk) {
		t.Fatalf("invalid json mangled: %q", bytes.Join(out, nil))
	}
}

func TestSSE_MultiDataLineEventCollapsed(t *testing.T) {
	// Documented rebuildSSE invariant (CR-5): a multi-data-line event
	// that gets rewritten collapses to a single data: line carrying
	// the joined payload, and the event terminator is preserved.
	// The newline join must fall between JSON tokens (a raw newline
	// inside a JSON string value would make the payload invalid).
	s := newSSESanitizer()
	chunk := []byte("event: delta\ndata: {\"function\":{\"name\":\"task\",\ndata: \"arguments\":\"{\\\"task_id\\\":\\\"ed99969c-e722-4310-b127-0a8e2e2641fd\\\"}\"}}\n\n")
	out, evs := s.Push(chunk)
	if len(evs) != 1 {
		t.Fatalf("expected 1 strip event, got %d out=%q", len(evs), bytes.Join(out, nil))
	}
	joined := string(bytes.Join(out, nil))
	if strings.Contains(joined, "ed99969c") {
		t.Fatalf("uuid leaked: %q", joined)
	}
	if got := strings.Count(joined, "data:"); got != 1 {
		t.Fatalf("data: lines = %d, want 1 (collapsed): %q", got, joined)
	}
	if !strings.HasPrefix(joined, "event: delta\n") {
		t.Fatalf("event field lost: %q", joined)
	}
	if !strings.HasSuffix(joined, "\n\n") {
		t.Fatalf("event terminator lost: %q", joined)
	}
}

func TestSSE_IncompleteFirstKeyFragment(t *testing.T) {
	// CR-1 through the SSE path: task_id as the first key of an
	// arguments fragment split across deltas.
	s := newSSESanitizer()
	nameEvt := []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"task\",\"arguments\":\"\"}}]}}]}\n\n")
	if out, _ := s.Push(nameEvt); len(out) == 0 {
		t.Fatal("name chunk must be forwarded")
	}
	// Arguments fragment is cut mid-object with task_id as the first
	// key: exercises stripTaskIDFromIncomplete through the SSE path.
	frag := []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"task_id\\\":\\\"ed99969c-e722-4310-b127-0a8e2e2641fd\\\",\\\"subagent_type\\\":\\\"linter\\\"\"}}]}}]}\n\n")
	out, evs := s.Push(frag)
	if len(evs) != 1 {
		t.Fatalf("expected 1 strip event, got %d", len(evs))
	}
	joined := string(bytes.Join(out, nil))
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
	line := strings.TrimPrefix(strings.SplitN(joined, "\n", 2)[0], "data: ")
	if err := json.Unmarshal([]byte(line), &parsed); err != nil {
		t.Fatalf("rewritten event is not valid JSON: %v\n%s", err, joined)
	}
	args := parsed.Choices[0].Delta.ToolCalls[0].Function.Arguments
	if args != `{"subagent_type":"linter"` {
		t.Fatalf("arguments = %q, want clean {\"subagent_type\":\"linter\"", args)
	}
}

func TestSanitizeCompleteJSON_JSONAndXMLTogether(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"task","arguments":"{\"subagent_type\":\"linter\",\"task_id\":\"91416d27-10d9-45cc-a680-80ed290fe955\"}"}}]}}],"other":"<invoke name=\"task\"><parameter name=\"task_id\">c98a830a-d08d-455b-a184-78e1b27c9849</parameter></invoke>"}`)
	out, ev, ok := sanitizeCompleteJSON(body, "json")
	if !ok {
		t.Fatal("expected complete JSON")
	}
	if ev == nil {
		t.Fatal("expected strip event")
	}
	s := string(out)
	if strings.Contains(s, "91416d27-10d9-45cc-a680-80ed290fe955") {
		t.Fatalf("json task_id leaked: %s", s)
	}
	if strings.Contains(s, "c98a830a-d08d-455b-a184-78e1b27c9849") {
		t.Fatalf("xml task_id leaked (JSON rewrite discarded XML strip): %s", s)
	}
	if !json.Valid([]byte(s)) {
		t.Fatalf("output is not valid JSON: %s", s)
	}
}

func TestInterceptStreamNeverDropsFirstChunk(t *testing.T) {
	nameEvt := []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"function\":{\"name\":\"task\",\"arguments\":\"\"}}]}}]}\n\n")
	raw, err := json.Marshal(map[string]any{
		"Body":       nameEvt,
		"ChunkIndex": 0,
		"Model":      "grok-4.6",
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := interceptStream(raw)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("envelope not ok: %s", out)
	}
	var resp interceptResp
	if len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, &resp); err != nil {
			t.Fatal(err)
		}
	}
	if resp.DropChunk {
		t.Fatal("DropChunk on first payload causes empty_stream")
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
