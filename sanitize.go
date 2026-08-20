package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const sessionPrefix = "ses"

var (
	uuidRE = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	// `\\*` tolerates JSON-escaped quotes so the pass also matches
	// XML embedded in JSON string values (e.g. name=\"task_id\").
	xmlRE = regexp.MustCompile(`(?is)<parameter\s+name=\\*["']task_id\\*["']\s*>\s*([^<]*?)\s*</parameter>`)
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
			return "null", true
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
	out := raw
	if changed {
		// Re-marshal without HTML escaping so that "<" in string
		// values (e.g. XML-style tool call bodies) stays literal and
		// the XML pass below can still see and strip it.
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			return raw, nil, true
		}
		out = bytes.TrimRight(buf.Bytes(), "\n")
	}
	// Run the XML pass on the (possibly rewritten) body so a JSON
	// rewrite no longer discards a simultaneous XML task_id strip.
	xmlOut, xmlEv, xmlChanged := stripXMLTaskID(string(out), source)
	if xmlChanged {
		if ev == nil {
			ev = xmlEv
		}
		return []byte(xmlOut), ev, true
	}
	if !changed {
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

// sseSanitizer rewrites one stream chunk at a time. It is stateless:
// Push never holds a chunk across calls (CPA treats a missing first
// payload as empty_stream and then marks the upstream auth
// unavailable).
type sseSanitizer struct{}

func newSSESanitizer() *sseSanitizer {
	return &sseSanitizer{}
}

func (s *sseSanitizer) Push(chunk []byte) (out [][]byte, events []*stripEvent) {
	if len(bytes.TrimSpace(chunk)) == 0 {
		return [][]byte{chunk}, nil
	}
	rest := chunk
	for {
		event, next, ok := splitSSE(rest)
		if !ok {
			if len(rest) == 0 {
				break
			}
			flushed, evs := s.handleEvent(rest)
			out = append(out, flushed...)
			events = append(events, evs...)
			break
		}
		rest = next
		flushed, evs := s.handleEvent(event)
		out = append(out, flushed...)
		events = append(events, evs...)
	}
	if len(out) == 0 {
		return [][]byte{chunk}, events
	}
	return out, events
}

var (
	taskIDJSONRE  = regexp.MustCompile(`(?i)(?:,\s*)?"task_id"\s*:\s*"(?:\\.|[^"\\])*"|(?:,\s*)?"task_id"\s*:\s*[^,}\]\s"]+`)
	taskIDValueRE = regexp.MustCompile(`(?i)"task_id"\s*:\s*"((?:\\.|[^"\\])*)"`)
)

// stripTaskIDFromIncomplete removes task_id members from an arguments
// JSON fragment that may be cut mid-object. taskIDJSONRE consumes a
// *leading* comma when the member is preceded by one; when the member
// is the first visible key of its object there is no leading comma, so
// the *trailing* comma must be swallowed instead. Without that,
// `{"task_id":"x","a":1}` would become the invalid fragment `{,"a":1}`.
//
// "First visible key" is decided by the last non-space byte already
// emitted to the output buffer, not by scanning the input: duplicate
// task_id keys back to back form a chain where each removal promotes
// the next member to first-key position. A fragment that starts
// mid-object (non-cumulative SSE deltas never contain the opening
// brace) is also treated as first-key, so its trailing comma is
// swallowed as well.
func stripTaskIDFromIncomplete(args string) (string, *stripEvent, bool) {
	// Case-insensitive fast path: the member regexes match (?i), so a
	// "Task_ID" key must not bypass the strip here.
	if !strings.Contains(strings.ToLower(args), "task_id") {
		return args, nil, false
	}
	// Best-effort extract of the value for logging.
	removed := ""
	if m := taskIDValueRE.FindStringSubmatch(args); len(m) == 2 {
		removed = m[1]
		if isSessionID(removed) {
			return args, nil, false
		}
	}
	matches := taskIDJSONRE.FindAllStringIndex(args, -1)
	if len(matches) == 0 {
		return args, nil, false
	}
	var b strings.Builder
	last := 0
	lastNonSpace := byte('{')
	updateTail := func(s string) {
		for i := len(s) - 1; i >= 0; i-- {
			if c := s[i]; c != ' ' && c != '\t' && c != '\r' && c != '\n' {
				lastNonSpace = c
				return
			}
		}
	}
	for _, m := range matches {
		start, end := m[0], m[1]
		if end <= last {
			// Entirely consumed by a previous comma swallow
			// (duplicate task_id keys back to back).
			continue
		}
		if start < last {
			// Partially overlapped by a previous comma swallow;
			// never slice backwards — args[last:start] would panic.
			start = last
		}
		chunk := args[last:start]
		b.WriteString(chunk)
		updateTail(chunk)
		if lastNonSpace == '{' {
			// First visible key of its object: swallow the trailing
			// comma (if any) so no dangling comma is left behind.
			rest := args[end:]
			trimmed := strings.TrimLeft(rest, " \t\r\n")
			if strings.HasPrefix(trimmed, ",") {
				end += len(rest) - len(trimmed) + 1
			}
		}
		last = end
	}
	b.WriteString(args[last:])
	next := b.String()
	if next == args {
		return args, nil, false
	}
	return next, &stripEvent{Tool: "task", Removed: removed, Reason: "not_ses_prefix", Source: "sse-incomplete"}, true
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
		return [][]byte{event}, nil
	}
	if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
		return [][]byte{event}, nil
	}
	if !json.Valid(payload) {
		return [][]byte{event}, nil
	}
	var root any
	if err := json.Unmarshal(payload, &root); err != nil {
		return [][]byte{event}, nil
	}

	// Whole-object tool call (non-delta) — sanitize in place.
	sanitized, ev, complete := sanitizeCompleteJSON(payload, "sse")
	if complete && ev != nil {
		return [][]byte{rebuildSSE(event, sanitized)}, []*stripEvent{ev}
	}

	changed, sev := stripTaskIDInArgTree(root)
	if !changed || sev == nil {
		return [][]byte{event}, nil
	}
	outJSON, err := json.Marshal(root)
	if err != nil {
		return [][]byte{event}, []*stripEvent{sev}
	}
	return [][]byte{rebuildSSE(event, outJSON)}, []*stripEvent{sev}
}

func stripTaskIDInArgTree(v any) (bool, *stripEvent) {
	switch t := v.(type) {
	case map[string]any:
		name := stringField(t, "name")
		if fn, ok := t["function"].(map[string]any); ok && name == "" {
			name = stringField(fn, "name")
		}
		skip := name != "" && !looksLikeTaskName(name)
		if !skip {
			for _, key := range []string{"arguments", "partial_json"} {
				raw, ok := t[key].(string)
				if !ok || raw == "" || !strings.Contains(raw, "task_id") {
					continue
				}
				next, ev, complete := sanitizeArgumentsJSON(raw)
				if !complete {
					next, ev, complete = stripTaskIDFromIncomplete(raw)
				}
				if complete && ev != nil {
					t[key] = next
					return true, ev
				}
			}
			if fn, ok := t["function"].(map[string]any); ok {
				if changed, ev := stripTaskIDInArgTree(fn); changed {
					return true, ev
				}
			}
		}
		for k, child := range t {
			if k == "function" {
				continue
			}
			if changed, ev := stripTaskIDInArgTree(child); changed {
				return true, ev
			}
		}
	case []any:
		for _, child := range t {
			if changed, ev := stripTaskIDInArgTree(child); changed {
				return true, ev
			}
		}
	}
	return false, nil
}

// rebuildSSE rewrites an SSE event whose payload was sanitized. It
// assumes the common single-data-line event shape: all data: lines are
// collapsed into the first one (multi-line data events would change
// framing), and the trailing blank line is preserved.
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
