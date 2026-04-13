package minime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type ScriptRunRequest struct {
	SessionID          string
	RepoRoot           string
	PythonExecutable   string
	RelativeScriptPath string
	Args               []string
	Environment        map[string]string
}

type ScriptRunner interface {
	Run(ctx context.Context, request ScriptRunRequest) (string, error)
}

type LocalScriptRunner struct{}

func (LocalScriptRunner) Run(ctx context.Context, request ScriptRunRequest) (string, error) {
	pythonExecutable := strings.TrimSpace(request.PythonExecutable)
	if pythonExecutable == "" {
		pythonExecutable = "python3"
	}

	commandArgs := append([]string{"-u", filepath.Join(request.RepoRoot, request.RelativeScriptPath)}, request.Args...)
	command := exec.CommandContext(ctx, pythonExecutable, commandArgs...)
	command.Dir = request.RepoRoot

	envVars := append([]string(nil), os.Environ()...)
	for key, value := range request.Environment {
		envVars = append(envVars, key+"="+value)
	}
	command.Env = envVars

	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output

	if err := command.Run(); err != nil {
		return strings.TrimSpace(output.String()), err
	}
	return strings.TrimSpace(output.String()), nil
}

type RemoteScriptRunner struct {
	BaseURL    string
	AuthToken  string
	HTTPClient *http.Client
}

type remoteScriptRunRequest struct {
	SessionID          string            `json:"session_id"`
	RepoRoot           string            `json:"repo_root"`
	PythonExecutable   string            `json:"python_executable,omitempty"`
	RelativeScriptPath string            `json:"relative_script_path"`
	Args               []string          `json:"args,omitempty"`
	Environment        map[string]string `json:"environment,omitempty"`
}

type remoteScriptRunResponse struct {
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

func (r RemoteScriptRunner) Run(ctx context.Context, request ScriptRunRequest) (string, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(r.BaseURL), "/")
	if baseURL == "" {
		return "", fmt.Errorf("remote script runner URL is required")
	}

	payload := remoteScriptRunRequest{
		SessionID:          request.SessionID,
		RepoRoot:           request.RepoRoot,
		PythonExecutable:   request.PythonExecutable,
		RelativeScriptPath: request.RelativeScriptPath,
		Args:               append([]string(nil), request.Args...),
		Environment:        request.Environment,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal remote runner request: %w", err)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/minime/scripts:run", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build remote runner request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if token := strings.TrimSpace(r.AuthToken); token != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+token)
	}

	client := r.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	response, err := client.Do(httpRequest)
	if err != nil {
		return "", fmt.Errorf("execute remote runner request: %w", err)
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return "", fmt.Errorf("read remote runner response: %w", err)
	}

	var decoded remoteScriptRunResponse
	if len(responseBody) > 0 {
		if unmarshalErr := json.Unmarshal(responseBody, &decoded); unmarshalErr != nil {
			if response.StatusCode >= http.StatusBadRequest {
				return strings.TrimSpace(string(responseBody)), fmt.Errorf("remote runner returned status %d", response.StatusCode)
			}
			return "", fmt.Errorf("decode remote runner response: %w", unmarshalErr)
		}
	}

	if response.StatusCode >= http.StatusBadRequest {
		message := strings.TrimSpace(decoded.Error)
		if message == "" {
			message = strings.TrimSpace(string(responseBody))
		}
		return strings.TrimSpace(decoded.Output), fmt.Errorf("remote runner returned status %d: %s", response.StatusCode, message)
	}

	return strings.TrimSpace(decoded.Output), nil
}

func scriptRunnerForConfig(config Config) (ScriptRunner, error) {
	switch strings.TrimSpace(strings.ToLower(config.ScriptRunnerMode)) {
	case "", "local":
		return LocalScriptRunner{}, nil
	case "remote":
		if strings.TrimSpace(config.ScriptRunnerURL) == "" {
			return nil, fmt.Errorf("MINIME_SCRIPT_RUNNER_URL is required when MINIME_SCRIPT_RUNNER_MODE=remote")
		}
		return RemoteScriptRunner{
			BaseURL:   config.ScriptRunnerURL,
			AuthToken: config.ScriptRunnerToken,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported script runner mode %q", config.ScriptRunnerMode)
	}
}
