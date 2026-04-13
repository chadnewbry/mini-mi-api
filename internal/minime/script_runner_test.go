package minime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestScriptRunnerForConfigDefaultsToLocal(t *testing.T) {
	t.Parallel()

	runner, err := scriptRunnerForConfig(Config{})
	if err != nil {
		t.Fatalf("build script runner: %v", err)
	}
	if _, ok := runner.(LocalScriptRunner); !ok {
		t.Fatalf("expected LocalScriptRunner, got %T", runner)
	}
}

func TestScriptRunnerForConfigRemoteRequiresURL(t *testing.T) {
	t.Parallel()

	_, err := scriptRunnerForConfig(Config{ScriptRunnerMode: "remote"})
	if err == nil {
		t.Fatal("expected missing url error")
	}
}

func TestRemoteScriptRunnerRun(t *testing.T) {
	t.Parallel()

	var receivedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/minime/scripts:run" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method %q", r.Method)
		}
		var payload remoteScriptRunRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload.RelativeScriptPath != "scripts/bootstrap_main_agent_creation.py" {
			t.Fatalf("unexpected script path %q", payload.RelativeScriptPath)
		}
		_ = json.NewEncoder(w).Encode(remoteScriptRunResponse{Output: "ok"})
	}))
	defer server.Close()

	runner := RemoteScriptRunner{
		BaseURL:   server.URL,
		AuthToken: "token-123",
	}
	output, err := runner.Run(context.Background(), ScriptRunRequest{
		SessionID:          "session-1",
		RepoRoot:           "/repo",
		RelativeScriptPath: "scripts/bootstrap_main_agent_creation.py",
	})
	if err != nil {
		t.Fatalf("run remote script: %v", err)
	}
	if output != "ok" {
		t.Fatalf("expected output ok, got %q", output)
	}
	if receivedAuth != "Bearer token-123" {
		t.Fatalf("expected bearer auth, got %q", receivedAuth)
	}
}
