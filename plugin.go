package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"
)

const (
	abiVersion    uint32 = 1
	schemaVersion uint32 = 1
	pluginName           = "task-id-sanitize"
	pluginVersion        = "0.1.2"
	defaultLog           = "/opt/cli-proxy-api/logs/task-id-sanitize.log"
)

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      map[string]any         `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	ResponseInterceptor    bool `json:"response_interceptor"`
	StreamChunkInterceptor bool `json:"response_stream_interceptor"`
}

type interceptReq struct {
	RequestID       string         `json:"RequestID"`
	SourceFormat    string         `json:"SourceFormat"`
	Model           string         `json:"Model"`
	RequestedModel  string         `json:"RequestedModel"`
	OriginalRequest []byte         `json:"OriginalRequest"`
	RequestBody     []byte         `json:"RequestBody"`
	Body            []byte         `json:"Body"`
	HistoryChunks   [][]byte       `json:"HistoryChunks"`
	ChunkIndex      int            `json:"ChunkIndex"`
	Metadata        map[string]any `json:"Metadata"`
}

type interceptResp struct {
	Body      []byte `json:"Body,omitempty"`
	DropChunk bool   `json:"DropChunk,omitempty"`
}

var (
	logMu   sync.Mutex
	logPath = defaultLog

	streamMu    sync.Mutex
	streams     = map[string]*sseSanitizer{}
	lastVersion string
)

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(_ *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	plugin.abi_version = C.uint32_t(abiVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	resetRuntime("shutdown")
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		configure(request, method)
		reg := pluginRegistration()
		raw, err := okEnvelope(reg)
		if err == nil {
			writeLog(map[string]any{
				"ts":     time.Now().UTC().Format(time.RFC3339Nano),
				"event":  "plugin.register.payload",
				"method": method,
				"body":   json.RawMessage(raw),
			})
		}
		return raw, err
	case "response.intercept_after":
		return interceptJSON(request)
	case "response.intercept_stream_chunk":
		return interceptStream(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func resetRuntime(reason string) {
	streamMu.Lock()
	n := len(streams)
	streams = map[string]*sseSanitizer{}
	streamMu.Unlock()
	writeLog(map[string]any{
		"ts":      time.Now().UTC().Format(time.RFC3339Nano),
		"event":   "plugin.reset",
		"reason":  reason,
		"version": pluginVersion,
		"streams": n,
	})
}

func configure(raw []byte, method string) {
	var req lifecycleRequest
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &req)
	}
	if p := os.Getenv("TASK_ID_SANITIZE_LOG"); p != "" {
		logPath = p
	}
	prev := lastVersion
	writeLog(map[string]any{
		"ts":       time.Now().UTC().Format(time.RFC3339Nano),
		"event":    "plugin.configure",
		"method":   method,
		"version":  pluginVersion,
		"previous": prev,
		"log":      logPath,
	})
	if prev != "" && prev != pluginVersion {
		writeLog(map[string]any{
			"ts":       time.Now().UTC().Format(time.RFC3339Nano),
			"event":    "plugin.upgrade",
			"from":     prev,
			"to":       pluginVersion,
			"method":   method,
		})
	}
	lastVersion = pluginVersion
	resetRuntime(method)
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: schemaVersion,
		// CPA 7.2.97 unmarshals pluginapi.Metadata with json tags
		// (github_repository). Some hosts also accept exported field names.
		Metadata: map[string]any{
			"name":              pluginName,
			"version":           pluginVersion,
			"author":            "Xell79",
			"github_repository": "https://github.com/Xell79/task-id-sanitize",
			"Name":              pluginName,
			"Version":           pluginVersion,
			"Author":            "Xell79",
			"GitHubRepository":  "https://github.com/Xell79/task-id-sanitize",
		},
		Capabilities: registrationCapability{
			ResponseInterceptor:    true,
			StreamChunkInterceptor: true,
		},
	}
}

func interceptJSON(raw []byte) ([]byte, error) {
	var req interceptReq
	if err := json.Unmarshal(raw, &req); err != nil {
		return okEnvelope(interceptResp{})
	}
	out, ev, ok := sanitizeCompleteJSON(req.Body, "json")
	if !ok {
		out2, ev2, changed := stripXMLTaskID(string(req.Body), "json+xml")
		if changed {
			logStrip(ev2, req, -1)
			return okEnvelope(interceptResp{Body: []byte(out2)})
		}
		return okEnvelope(interceptResp{})
	}
	if ev != nil {
		logStrip(ev, req, -1)
		return okEnvelope(interceptResp{Body: out})
	}
	return okEnvelope(interceptResp{})
}

func interceptStream(raw []byte) ([]byte, error) {
	var req interceptReq
	if err := json.Unmarshal(raw, &req); err != nil {
		return okEnvelope(interceptResp{})
	}
	if req.ChunkIndex == -1 {
		return okEnvelope(interceptResp{})
	}
	if len(req.Body) == 0 {
		return okEnvelope(interceptResp{})
	}

	key := streamKey(req)
	streamMu.Lock()
	s := streams[key]
	if s == nil {
		s = newSSESanitizer()
		streams[key] = s
	}
	streamMu.Unlock()

	body := req.Body
	var events []*stripEvent
	var pieces [][]byte

	if looksLikeSSE(body) || bytes.Contains(body, []byte("data:")) {
		streamMu.Lock()
		pieces, events = s.Push(body)
		if bytes.Contains(body, []byte("[DONE]")) {
			rest, ev2 := s.Flush()
			pieces = append(pieces, rest...)
			events = append(events, ev2...)
			delete(streams, key)
		}
		streamMu.Unlock()
		if len(pieces) == 0 {
			for _, ev := range events {
				logStrip(ev, req, req.ChunkIndex)
			}
			// Never DropChunk: CPA treats a missing first payload as
			// empty_stream, retries, and marks xAI auth unavailable.
			return okEnvelope(interceptResp{})
		}
		joined := bytes.Join(pieces, nil)
		for _, ev := range events {
			ev.ChunkIdx = req.ChunkIndex
			logStrip(ev, req, req.ChunkIndex)
		}
		if bytes.Equal(joined, body) && len(events) == 0 {
			return okEnvelope(interceptResp{})
		}
		return okEnvelope(interceptResp{Body: joined})
	}

	out, ev, ok := sanitizeCompleteJSON(body, "stream-json")
	if ok && ev != nil {
		logStrip(ev, req, req.ChunkIndex)
		return okEnvelope(interceptResp{Body: out})
	}
	return okEnvelope(interceptResp{})
}

func streamKey(req interceptReq) string {
	if id := strings.TrimSpace(req.RequestID); id != "" {
		return id + "|" + req.Model
	}
	seed := req.OriginalRequest
	if len(seed) == 0 {
		seed = req.RequestBody
	}
	if len(seed) > 0 {
		sum := 0
		for _, b := range seed {
			sum = sum*31 + int(b)
		}
		return fmt.Sprintf("%s|%d|%d", req.Model, len(seed), sum)
	}
	if req.Metadata != nil {
		for _, k := range []string{"request_id", "requestID"} {
			if v, ok := req.Metadata[k]; ok {
				if s, ok := v.(string); ok && s != "" {
					return s + "|" + req.Model
				}
			}
		}
	}
	return req.Model + "|" + req.RequestedModel
}

func logStrip(ev *stripEvent, req interceptReq, chunk int) {
	if ev == nil {
		return
	}
	ev.Model = req.Model
	if chunk >= 0 {
		ev.ChunkIdx = chunk
	}
	writeLog(map[string]any{
		"ts":           time.Now().UTC().Format(time.RFC3339Nano),
		"event":        "task_id.stripped",
		"tool":         ev.Tool,
		"removed":      ev.Removed,
		"reason":       ev.Reason,
		"source":       ev.Source,
		"model":        req.Model,
		"requested":    req.RequestedModel,
		"chunk_index":  ev.ChunkIdx,
		"held_events":  ev.HeldN,
		"uuid_like":    uuidRE.MatchString(ev.Removed),
	})
}

func writeLog(row map[string]any) {
	b, err := json.Marshal(row)
	if err != nil {
		return
	}
	b = append(b, '\n')
	logMu.Lock()
	defer logMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = f.Write(b)
	_ = f.Close()
}

func okEnvelope(v any) ([]byte, error) {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
