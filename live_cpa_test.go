//go:build live

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func liveBaseURL() string {
	if v := strings.TrimSpace(os.Getenv("CPA_BASE_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://127.0.0.1:8320/v1"
}

func liveAPIKey(t *testing.T) string {
	t.Helper()
	key := strings.TrimSpace(os.Getenv("CPA_API_KEY"))
	if key == "" {
		t.Skip("CPA_API_KEY unset")
	}
	return key
}

func liveModel() string {
	if v := strings.TrimSpace(os.Getenv("CPA_MODEL")); v != "" {
		return v
	}
	return "grok-4.6"
}

func liveClient() *http.Client {
	return &http.Client{Timeout: 45 * time.Second}
}

func TestLiveCPA_Models(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, liveBaseURL()+"/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+liveAPIKey(t))
	resp, err := liveClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /models status=%d body=%s", resp.StatusCode, truncate(body, 400))
	}
	if !bytes.Contains(body, []byte(`"data"`)) && !bytes.Contains(body, []byte(`"id"`)) {
		t.Fatalf("unexpected models payload: %s", truncate(body, 400))
	}
}

func TestLiveCPA_StreamFirstPayload(t *testing.T) {
	payload := map[string]any{
		"model":  liveModel(),
		"stream": true,
		"messages": []map[string]string{
			{"role": "user", "content": "Reply with the single word ok."},
		},
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, liveBaseURL()+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+liveAPIKey(t))
	req.Header.Set("Content-Type", "application/json")
	resp, err := liveClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := string(body)
		if strings.Contains(msg, "empty_stream") || strings.Contains(msg, "auth_unavailable") {
			t.Fatalf("proxy auth/stream broken: status=%d body=%s", resp.StatusCode, truncate(body, 600))
		}
		t.Fatalf("POST /chat/completions status=%d body=%s", resp.StatusCode, truncate(body, 600))
	}

	gotPayload := false
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "empty_stream") || strings.Contains(line, "auth_unavailable") {
			t.Fatalf("stream error line: %s", line)
		}
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data != "" && data != "[DONE]" {
				gotPayload = true
				break
			}
		}
	}
	if err := scanner.Err(); err != nil && !gotPayload {
		t.Fatalf("stream read: %v", err)
	}
	if !gotPayload {
		t.Fatal("upstream stream closed before first payload")
	}
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
