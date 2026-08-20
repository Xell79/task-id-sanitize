package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

const sessionPrefix = "ses"

var (
	uuidRE = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	xmlRE  = regexp.MustCompile(`(?is)<parameter\s+name=["']task_id["']\s*>\s*([^<]*?)\s*</parameter>`)
)

type stripEvent struct {
	Tool     string `json:"tool"`
	Removed  string `json:"removed"`
	Reason   string `json:"reason"`
	Source   string `json:"source"`
	Model    string `json:"model,omitempty"`
	HeldN    int    `json:"held_events,omitempty"`
	ChunkIdx int    `json:"chunk_index,omitempty"`
}

func isSessionID(v string) bool {
	return strings.HasPrefix(strings.TrimSpace(v), sessionPrefix)
}

func shouldStripTaskID(v any) (string, bool) {
	s, ok := v.(string)
	if !ok {
		if v == nil {
			return "", true
		}
		return fmt.Sprint(v), true
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return s, true
	}
	if isSessionID(s) {
		return s, false
	}
	return s, true
}

func sanitizeArgumentsJSON(raw string) (string, *stripEvent, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !json.Valid([]byte(trimmed)) {
		return raw, nil, false
	}
	var v any
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		return raw, nil, false
	}
	changed, ev := stripTaskIDValue(v, "arguments")
	if !changed {
		return raw, nil, true
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw, nil, true
	}
	return string(out), ev, true
}

func stripTaskIDValue(v any, source string) (bool, *stripEvent) {
	switch t := v.(type) {
	case map[string]any:
		changed := false
		var ev *stripEvent
		if raw, ok := t["task_id"]; ok {
			if removed, strip := shouldStripTaskID(raw); strip {
				delete(t, "task_id")
				changed = true
				ev = &stripEvent{Tool: "task", Removed: removed, Reason: "not_ses_prefix", Source: source}
			}
		}
		for _, child := range t {
			c, e := stripTaskIDValue(child, source)
			if c {
				changed = true
				if ev == nil {
					ev = e
				}
			}
		}
		return changed, ev
	case []any:
		changed := false
		var ev *stripEvent
		for _, child := range t {
			c, e := stripTaskIDValue(child, source)
			if c {
				changed = true
				if ev == nil {
					ev = e
				}
			}
		}
		return changed, ev
	default:
		return false, nil
	}
}

func looksLikeTaskName(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), "task")
}

func sanitizeCompleteJSON(raw []byte, source string) ([]byte, *stripEvent, bool) {
	if len(bytes.TrimSpace(raw)) == 0 || !json.Valid(raw) {
		return raw, nil, false
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw, nil, false
	}
	changed, ev := walkToolCalls(v, source)
	xmlOut, xmlEv, xmlChanged := stripXMLTaskID(string(raw), source)
	if xmlChanged && !changed {
		return []byte(xmlOut), xmlEv, true
	}
	if !changed {
		return raw, nil, true
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw, nil, true
	}
	return out, ev, true
}

func walkToolCalls(v any, source string) (bool, *stripEvent) {
	switch t := v.(type) {
	case map[string]any:
		changed := false
		var ev *stripEvent

		name := stringField(t, "name")
		if looksLikeTaskName(name) {
			if args, ok := t["arguments"].(string); ok {
				next, e, complete := sanitizeArgumentsJSON(args)
				if complete && e != nil {
					t["arguments"] = next
					changed = true
					ev = e
					ev.Source = source
				}
			}
			if input, ok := t["input"].(map[string]any); ok {
				c, e := stripTaskIDValue(input, source)
				if c {
					changed = true
					if ev == nil {
						ev = e
					}
				}
			}
		}

		if raw, ok := t["task_id"]; ok && (looksLikeTaskName(name) || hasTaskShape(t)) {
			if removed, strip := shouldStripTaskID(raw); strip {
				delete(t, "task_id")
				changed = true
				if ev == nil {
					ev = &stripEvent{Tool: "task", Removed: removed, Reason: "not_ses_prefix", Source: source}
				}
			}
		}

		for _, child := range t {
			c, e := walkToolCalls(child, source)
			if c {
				changed = true
				if ev == nil {
					ev = e
				}
			}
		}
		return changed, ev
	case []any:
		changed := false
		var ev *stripEvent
		for _, child := range t {
			c, e := walkToolCalls(child, source)
			if c {
				changed = true
				if ev == nil {
					ev = e
				}
			}
		}
		return changed, ev
	default:
		return false, nil
	}
}

func hasTaskShape(t map[string]any) bool {
	_, hasDesc := t["description"]
	_, hasPrompt := t["prompt"]
	_, hasSub := t["subagent_type"]
	return hasDesc && hasPrompt && hasSub
}

func stringField(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

func stripXMLTaskID(s, source string) (string, *stripEvent, bool) {
	changed := false
	var ev *stripEvent
	out := xmlRE.ReplaceAllStringFunc(s, func(match string) string {
		sub := xmlRE.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		val := strings.TrimSpace(sub[1])
		if isSessionID(val) {
			return match
		}
		changed = true
		ev = &stripEvent{Tool: "task", Removed: val, Reason: "not_ses_prefix", Source: source + "+xml"}
		return ""
	})
	return out, ev, changed
}

// sseSanitizer holds incomplete SSE events until a task tool-call's
// arguments JSON is complete, then strips a bogus task_id.
type sseSanitizer struct {
	buf      []byte
	calls    map[string]*callAcc
	pending  [][]byte
	holdTask bool
}

type callAcc struct {
	id        string
	index     string
	name      string
	args      strings.Builder
	held      [][]byte
	namedTask bool
}

func newSSESanitizer() *sseSanitizer {
	return &sseSanitizer{calls: make(map[string]*callAcc)}
}

func callKey(index any, id string) string {
	if index != nil && fmt.Sprint(index) != "<nil>" && fmt.Sprint(index) != "" {
		return fmt.Sprintf("idx:%v", index)
	}
	if id != "" {
		return "id:" + id
	}
	return "idx:0"
}

func (s *sseSanitizer) Push(chunk []byte) (out [][]byte, events []*stripEvent) {
	if len(bytes.TrimSpace(chunk)) == 0 {
		return [][]byte{chunk}, nil
	}
	s.buf = append(s.buf, chunk...)
	for {
		event, rest, ok := splitSSE(s.buf)
		if !ok {
			break
		}
		s.buf = rest
		flushed, evs := s.handleEvent(event)
		out = append(out, flushed...)
		events = append(events, evs...)
	}
	return out, events
}

func (s *sseSanitizer) Flush() (out [][]byte, events []*stripEvent) {
	if len(s.buf) > 0 {
		flushed, evs := s.handleEvent(s.buf)
		out = append(out, flushed...)
		events = append(events, evs...)
		s.buf = nil
	}
	flushed, evs := s.flushHeldSanitized()
	out = append(out, flushed...)
	events = append(events, evs...)
	if len(s.pending) > 0 {
		out = append(out, s.pending...)
		s.pending = nil
	}
	return out, events
}

var taskIDJSONRE = regexp.MustCompile(`(?i),"task_id"\s*:\s*"(?:\\.|[^"\\])*"|,"task_id"\s*:\s*[^,}\]]+|"task_id"\s*:\s*"(?:\\.|[^"\\])*",|"task_id"\s*:\s*[^,}\]]+,`)

func stripTaskIDFromIncomplete(args string) (string, *stripEvent, bool) {
	if !strings.Contains(args, "task_id") {
		return args, nil, false
	}
	// Best-effort extract of the value for logging.
	removed := ""
	if m := regexp.MustCompile(`(?i)"task_id"\s*:\s*"((?:\\.|[^"\\])*)"`).FindStringSubmatch(args); len(m) == 2 {
		removed = m[1]
		if isSessionID(removed) {
			return args, nil, false
		}
	}
	next := taskIDJSONRE.ReplaceAllString(args, "")
	if next == args {
		return args, nil, false
	}
	return next, &stripEvent{Tool: "task", Removed: removed, Reason: "not_ses_prefix", Source: "sse-incomplete"}, true
}

func (s *sseSanitizer) flushHeldSanitized() (out [][]byte, events []*stripEvent) {
	for key, c := range s.calls {
		if len(c.held) == 0 {
			continue
		}
		acc := c.args.String()
		if next, ev, complete := sanitizeArgumentsJSON(acc); complete {
			if ev != nil {
				ev.HeldN = len(c.held)
				events = append(events, ev)
				out = append(out, rebuildTaskEvent(c.held[len(c.held)-1], c, next))
			} else {
				out = append(out, c.held...)
			}
			c.held = nil
			delete(s.calls, key)
			continue
		}
		if next, ev, stripped := stripTaskIDFromIncomplete(acc); stripped {
			ev.HeldN = len(c.held)
			events = append(events, ev)
			out = append(out, rebuildTaskEvent(c.held[len(c.held)-1], c, next))
			c.held = nil
			delete(s.calls, key)
			continue
		}
		out = append(out, c.held...)
		c.held = nil
		delete(s.calls, key)
	}
	return out, events
}

func splitSSE(buf []byte) (event, rest []byte, ok bool) {
	// SSE events end with a blank line.
	idx := bytes.Index(buf, []byte("\n\n"))
	if idx < 0 {
		idx = bytes.Index(buf, []byte("\r\n\r\n"))
		if idx < 0 {
			return nil, buf, false
		}
		return buf[:idx+4], buf[idx+4:], true
	}
	return buf[:idx+2], buf[idx+2:], true
}

func sseDataPayload(event []byte) []byte {
	var data [][]byte
	for _, line := range bytes.Split(event, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if bytes.HasPrefix(line, []byte("data:")) {
			payload := bytes.TrimSpace(line[len("data:"):])
			data = append(data, payload)
		}
	}
	if len(data) == 0 {
		return nil
	}
	return bytes.Join(data, []byte("\n"))
}

func (s *sseSanitizer) handleEvent(event []byte) (out [][]byte, events []*stripEvent) {
	payload := sseDataPayload(event)
	if len(payload) == 0 {
		return s.flushNonTask(event), nil
	}
	if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
		flushed, evs := s.flushHeldSanitized()
		flushed = append(flushed, event)
		return flushed, evs
	}
	if !json.Valid(payload) {
		return s.flushNonTask(event), nil
	}
	var root any
	if err := json.Unmarshal(payload, &root); err != nil {
		return s.flushNonTask(event), nil
	}

	deltas := collectArgDeltas(root)
	if len(deltas) == 0 {
		// Whole-object tool call (non-delta) — sanitize in place.
		sanitized, ev, complete := sanitizeCompleteJSON(payload, "sse")
		if complete && ev != nil {
			return [][]byte{rebuildSSE(event, sanitized)}, []*stripEvent{ev}
		}
		return s.flushNonTask(event), nil
	}

	var heldForTask bool
	for _, d := range deltas {
		key := callKey(d.Index, d.ID)
		c := s.calls[key]
		if c == nil {
			c = &callAcc{id: d.ID, index: fmt.Sprint(d.Index)}
			s.calls[key] = c
		}
		if d.ID != "" {
			c.id = d.ID
		}
		if d.Name != "" {
			c.name = d.Name
			c.namedTask = looksLikeTaskName(d.Name)
		}
		if d.Args != "" {
			c.args.WriteString(d.Args)
		}
		if c.namedTask || c.name == "" {
			c.held = append(c.held, event)
			heldForTask = true
		}
		if c.namedTask {
			acc := c.args.String()
			if next, ev, complete := sanitizeArgumentsJSON(acc); complete {
				if ev != nil {
					rebuilt := rebuildTaskEvent(event, c, next)
					ev.HeldN = len(c.held)
					c.held = nil
					c.args.Reset()
					c.args.WriteString(next)
					return [][]byte{rebuilt}, []*stripEvent{ev}
				}
				// complete and clean: release held as-is
				out = append(out, c.held...)
				c.held = nil
				return out, nil
			}
		} else if c.name != "" {
			// Named something other than task: release.
			out = append(out, c.held...)
			c.held = nil
			if !heldForTask {
				out = append(out, event)
			}
			return out, nil
		}
	}
	if heldForTask {
		return nil, nil
	}
	return [][]byte{event}, nil
}

func (s *sseSanitizer) flushNonTask(event []byte) [][]byte {
	var out [][]byte
	for _, c := range s.calls {
		if !c.namedTask && len(c.held) > 0 {
			out = append(out, c.held...)
			c.held = nil
		}
	}
	out = append(out, event)
	return out
}

type argDelta struct {
	ID    string
	Index any
	Name  string
	Args  string
}

func collectArgDeltas(v any) []argDelta {
	var out []argDelta
	var walk func(any)
	walk = func(node any) {
		m, ok := node.(map[string]any)
		if !ok {
			if arr, ok := node.([]any); ok {
				for _, c := range arr {
					walk(c)
				}
			}
			return
		}
		// OpenAI chat completions: choices[].delta.tool_calls[]
		if tcs, ok := m["tool_calls"].([]any); ok {
			for _, raw := range tcs {
				tm, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				d := argDelta{ID: stringField(tm, "id"), Index: tm["index"]}
				if fn, ok := tm["function"].(map[string]any); ok {
					d.Name = stringField(fn, "name")
					d.Args = stringField(fn, "arguments")
				}
				if d.Name == "" {
					d.Name = stringField(tm, "name")
				}
				if d.Args == "" {
					d.Args = stringField(tm, "arguments")
				}
				if d.Name != "" || d.Args != "" || d.ID != "" {
					out = append(out, d)
				}
			}
			return
		}
		if args, ok := m["arguments"].(string); ok && args != "" {
			d := argDelta{Args: args, Name: stringField(m, "name"), ID: stringField(m, "id")}
			if fn, ok := m["function"].(map[string]any); ok && d.Name == "" {
				d.Name = stringField(fn, "name")
			}
			out = append(out, d)
		}
		if partial, ok := m["partial_json"].(string); ok && partial != "" {
			out = append(out, argDelta{Args: partial, Name: stringField(m, "name"), ID: stringField(m, "id")})
		}
		for _, child := range m {
			walk(child)
		}
	}
	walk(v)
	return out
}

func rebuildSSE(original []byte, payload []byte) []byte {
	var b bytes.Buffer
	wroteData := false
	lines := bytes.Split(original, []byte("\n"))
	for i, line := range lines {
		raw := bytes.TrimRight(line, "\r")
		if bytes.HasPrefix(raw, []byte("data:")) {
			if !wroteData {
				b.WriteString("data: ")
				b.Write(payload)
				wroteData = true
			}
		} else if len(raw) > 0 {
			b.Write(raw)
		}
		if i < len(lines)-1 {
			b.WriteByte('\n')
		}
	}
	if !bytes.HasSuffix(b.Bytes(), []byte("\n\n")) && !bytes.HasSuffix(b.Bytes(), []byte("\r\n\r\n")) {
		if !bytes.HasSuffix(b.Bytes(), []byte("\n")) {
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	return b.Bytes()
}

func rebuildTaskEvent(original []byte, c *callAcc, args string) []byte {
	payload := sseDataPayload(original)
	var root any
	if err := json.Unmarshal(payload, &root); err != nil {
		// Fallback synthetic OpenAI chunk.
		syn, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{
				"index": 0,
				"delta": map[string]any{
					"tool_calls": []any{map[string]any{
						"index": 0,
						"id":    c.id,
						"type":  "function",
						"function": map[string]any{
							"name":      "task",
							"arguments": args,
						},
					}},
				},
			}},
		})
		return rebuildSSE(original, syn)
	}
	injectArgs(root, c, args)
	out, err := json.Marshal(root)
	if err != nil {
		return rebuildSSE(original, payload)
	}
	return rebuildSSE(original, out)
}

func injectArgs(v any, c *callAcc, args string) {
	m, ok := v.(map[string]any)
	if !ok {
		if arr, ok := v.([]any); ok {
			for _, child := range arr {
				injectArgs(child, c, args)
			}
		}
		return
	}
	if tcs, ok := m["tool_calls"].([]any); ok {
		for _, raw := range tcs {
			tm, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			fn, _ := tm["function"].(map[string]any)
			if fn == nil {
				fn = map[string]any{}
				tm["function"] = fn
			}
			if c.name != "" {
				fn["name"] = c.name
			}
			fn["arguments"] = args
			if c.id != "" {
				tm["id"] = c.id
			}
		}
	}
	if _, ok := m["arguments"].(string); ok {
		m["arguments"] = args
		if c.name != "" && stringField(m, "name") == "" {
			m["name"] = c.name
		}
	}
	for _, child := range m {
		injectArgs(child, c, args)
	}
}

func looksLikeSSE(b []byte) bool {
	t := bytes.TrimLeftFunc(b, unicode.IsSpace)
	return bytes.HasPrefix(t, []byte("data:")) || bytes.Contains(t[:min(len(t), 32)], []byte("data:"))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
