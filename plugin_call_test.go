package main

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// pluginCall is the pure-Go dispatch behind the cgo entry point; these
// tests cover the boundary contract (method validation, size guard,
// envelope shape) without cgo, which the toolchain does not support in
// _test.go files.

func decodeEnvelope(t *testing.T, raw []byte) envelope {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("envelope: %v raw=%s", err, raw)
	}
	return env
}

func TestPluginCall_EmptyMethod(t *testing.T) {
	raw, rc := pluginCall("", nil, 0)
	if rc != 1 {
		t.Fatalf("rc = %d, want 1", rc)
	}
	env := decodeEnvelope(t, raw)
	if env.OK || env.Error == nil || env.Error.Code != "invalid_method" {
		t.Fatalf("envelope = %+v, want invalid_method", env)
	}
}

func TestPluginCall_OversizedRequestGuard(t *testing.T) {
	// SEC-4: a declared size_t length above MaxInt32 must be rejected
	// before any C.GoBytes conversion could truncate it to a negative
	// C.int. The actual buffer is tiny; only the declared length is
	// hostile.
	raw, rc := pluginCall("response.intercept_after", []byte(`{}`), uint64(math.MaxInt32)+1)
	if rc != 1 {
		t.Fatalf("rc = %d, want 1", rc)
	}
	env := decodeEnvelope(t, raw)
	if env.OK || env.Error == nil || env.Error.Code != "invalid_request" {
		t.Fatalf("envelope = %+v, want invalid_request", env)
	}
	if !strings.Contains(env.Error.Message, "too large") {
		t.Fatalf("message = %q", env.Error.Message)
	}
}

func TestPluginCall_UnknownMethod(t *testing.T) {
	raw, rc := pluginCall("no.such.method", nil, 0)
	if rc != 0 {
		t.Fatalf("rc = %d, want 0 (unknown method is a soft error)", rc)
	}
	env := decodeEnvelope(t, raw)
	if env.OK || env.Error == nil || env.Error.Code != "unknown_method" {
		t.Fatalf("envelope = %+v, want unknown_method", env)
	}
}

func TestPluginCall_RegisterHappyPath(t *testing.T) {
	t.Setenv("TASK_ID_SANITIZE_LOG", t.TempDir()+"/plugincall-test.log")
	request := []byte(`{"config_yaml":{}}`)
	raw, rc := pluginCall("plugin.register", request, uint64(len(request)))
	if rc != 0 {
		t.Fatalf("rc = %d, want 0", rc)
	}
	env := decodeEnvelope(t, raw)
	if !env.OK {
		t.Fatalf("envelope not ok: %+v", env)
	}
	var reg registration
	if err := json.Unmarshal(env.Result, &reg); err != nil {
		t.Fatal(err)
	}
	if reg.SchemaVersion != schemaVersion {
		t.Fatalf("schema_version = %d", reg.SchemaVersion)
	}
	if reg.Metadata["name"] != pluginName || reg.Metadata["version"] != pluginVersion {
		t.Fatalf("metadata = %v %v", reg.Metadata["name"], reg.Metadata["version"])
	}
	if reg.Metadata["license"] != "MIT" || reg.Metadata["License"] != "MIT" {
		t.Fatalf("license metadata = %v / %v", reg.Metadata["license"], reg.Metadata["License"])
	}
	if !reg.Capabilities.ResponseInterceptor || !reg.Capabilities.StreamChunkInterceptor {
		t.Fatalf("capabilities = %+v", reg.Capabilities)
	}
	// Reconfigure must take the same path and stay a soft success.
	raw2, rc2 := pluginCall("plugin.reconfigure", nil, 0)
	if rc2 != 0 {
		t.Fatalf("reconfigure rc = %d, want 0", rc2)
	}
	if env2 := decodeEnvelope(t, raw2); !env2.OK {
		t.Fatalf("reconfigure envelope not ok: %+v", env2)
	}
	// Shutdown resets runtime state and must not panic.
	cliproxyPluginShutdown()
}

func TestPluginCall_InterceptAfterRoundTrip(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"task","arguments":"{\"task_id\":\"91416d27-10d9-45cc-a680-80ed290fe955\"}"}}]}}]}`)
	request, _ := json.Marshal(map[string]any{"Body": body, "Model": "grok-4.6"})
	raw, rc := pluginCall("response.intercept_after", request, uint64(len(request)))
	if rc != 0 {
		t.Fatalf("rc = %d, want 0", rc)
	}
	env := decodeEnvelope(t, raw)
	if !env.OK {
		t.Fatalf("envelope not ok: %+v", env)
	}
	var ir interceptResp
	if len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, &ir); err != nil {
			t.Fatal(err)
		}
	}
	if ir.DropChunk {
		t.Fatal("DropChunk must stay false")
	}
	if len(ir.Body) == 0 {
		t.Fatal("expected rewritten body")
	}
	if strings.Contains(string(ir.Body), "91416d27") {
		t.Fatalf("uuid leaked: %s", ir.Body)
	}
	if !json.Valid(ir.Body) {
		t.Fatalf("rewritten body invalid: %s", ir.Body)
	}
}
