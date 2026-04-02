package minime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type ScriptGenerator struct {
	RepoRoot             string
	PythonExecutable     string
	ImageGeneratorScript string
	StatePipelineScript  string
}

type scriptManifest struct {
	CreatedAt              time.Time         `json:"createdAt"`
	UpdatedAt              time.Time         `json:"updatedAt"`
	SourcePhotoPaths       []string          `json:"sourcePhotoPaths"`
	CandidateImagePaths    []string          `json:"candidateImagePaths"`
	CurrentCandidateIndex  *int              `json:"currentCandidateIndex"`
	TotalCandidates        *int              `json:"totalCandidates"`
	CurrentStepLabel       string            `json:"currentStepLabel"`
	GenerationLogPath      string            `json:"generationLogPath"`
	SelectedSourcePhotoPath string           `json:"selectedSourcePhotoPath"`
	SelectedCandidatePath  string            `json:"selectedCandidatePath"`
	PublishedPreviewPath   string            `json:"publishedPreviewPath"`
	StateAssetPaths        map[string]string `json:"stateAssetPaths"`
	StateSourceImagePaths  map[string]string `json:"stateSourceImagePaths"`
	Status                 string            `json:"status"`
	Notes                  string            `json:"notes"`
}

func (g ScriptGenerator) Bootstrap(ctx context.Context, env GenerationEnvironment, session *sessionRecord) error {
	if err := g.runMainAgentScript(
		ctx,
		session.ID,
		env,
		filepath.Join("scripts", "bootstrap_main_agent_creation.py"),
		nil,
	); err != nil {
		return err
	}

	manifest, _, err := g.syncSessionToWorkspace(ctx, env, session)
	if err != nil {
		return err
	}

	session.Status = manifest.Status
	session.CurrentStepLabel = manifest.CurrentStepLabel
	session.Notes = manifest.Notes
	return nil
}

func (g ScriptGenerator) GenerateCandidates(ctx context.Context, env GenerationEnvironment, session *sessionRecord) error {
	if _, _, err := g.syncSessionToWorkspace(ctx, env, session); err != nil {
		return err
	}
	if err := g.runMainAgentScript(
		ctx,
		session.ID,
		env,
		filepath.Join("scripts", "generate_main_agent_candidates.py"),
		nil,
	); err != nil {
		return err
	}

	manifest, workspaceRoot, err := g.readManifest(session.ID, env)
	if err != nil {
		return err
	}

	session.Candidates = nil
	candidateByPath := map[string]*assetRecord{}
	for _, path := range manifest.CandidateImagePaths {
		asset, err := env.ImportFile(session.ID, "candidate-renders", path)
		if err != nil {
			return err
		}
		session.Candidates = append(session.Candidates, asset)
		candidateByPath[filepath.Clean(path)] = asset
	}

	if manifest.SelectedCandidatePath != "" {
		if asset := candidateByPath[filepath.Clean(manifest.SelectedCandidatePath)]; asset != nil {
			session.SelectedCandidateID = asset.ID
			session.PublishedPreview = asset
		}
	}

	if manifest.GenerationLogPath != "" && filepath.IsAbs(manifest.GenerationLogPath) {
		if _, err := os.Stat(manifest.GenerationLogPath); err == nil {
			_, _ = env.ImportFile(session.ID, "logs", manifest.GenerationLogPath)
		}
	}

	_ = workspaceRoot
	session.CurrentIndex = manifest.CurrentCandidateIndex
	session.TotalCount = manifest.TotalCandidates
	session.Status = manifest.Status
	session.CurrentStepLabel = manifest.CurrentStepLabel
	session.Notes = manifest.Notes
	return nil
}

func (g ScriptGenerator) GenerateStates(ctx context.Context, env GenerationEnvironment, session *sessionRecord, states []string) error {
	if _, _, err := g.syncSessionToWorkspace(ctx, env, session); err != nil {
		return err
	}

	args := make([]string, 0, len(states)*2)
	for _, state := range states {
		args = append(args, "--state", state)
	}

	if err := g.runMainAgentScript(
		ctx,
		session.ID,
		env,
		filepath.Join("scripts", "run_main_agent_state_pipeline.py"),
		args,
	); err != nil {
		if strings.Contains(err.Error(), ErrNoSelectedAsset.Error()) {
			return ErrNoSelectedAsset
		}
		return err
	}

	manifest, _, err := g.readManifest(session.ID, env)
	if err != nil {
		return err
	}

	session.StateAssets = map[string]*stateAssetRecord{}
	for stateName, path := range manifest.StateAssetPaths {
		var sourceAsset *assetRecord
		if sourcePath := manifest.StateSourceImagePaths[stateName]; sourcePath != "" {
			importedSourceAsset, err := env.ImportFile(session.ID, filepath.Join("state-renders", "main-agent", stateName), sourcePath)
			if err != nil {
				return err
			}
			sourceAsset = importedSourceAsset
		}
		if sourceAsset == nil {
			sourceAsset = findAssetByID(session.Candidates, session.SelectedCandidateID)
		}
		if sourceAsset == nil {
			sourceAsset = findAssetByID(session.SourcePhotos, session.SelectedSourcePhotoID)
		}
		finalAsset, err := env.ImportFile(session.ID, filepath.Join("state-renders", "main-agent", stateName), path)
		if err != nil {
			return err
		}
		session.StateAssets[stateName] = &stateAssetRecord{
			StateName: stateName,
			Source:    sourceAsset,
			Final:     finalAsset,
		}
	}

	session.CurrentIndex = manifest.CurrentCandidateIndex
	session.TotalCount = manifest.TotalCandidates
	session.Status = manifest.Status
	session.CurrentStepLabel = manifest.CurrentStepLabel
	session.Notes = manifest.Notes
	return nil
}

func (g ScriptGenerator) syncSessionToWorkspace(ctx context.Context, env GenerationEnvironment, session *sessionRecord) (scriptManifest, string, error) {
	if err := g.runMainAgentScript(
		ctx,
		session.ID,
		env,
		filepath.Join("scripts", "bootstrap_main_agent_creation.py"),
		nil,
	); err != nil {
		return scriptManifest{}, "", err
	}

	manifest, workspaceRoot, err := g.readManifest(session.ID, env)
	if err != nil {
		return scriptManifest{}, "", err
	}

	manifest.SourcePhotoPaths = assetPaths(session.SourcePhotos)
	manifest.CandidateImagePaths = assetPaths(session.Candidates)
	manifest.SelectedSourcePhotoPath = assetPathByID(session.SourcePhotos, session.SelectedSourcePhotoID)
	manifest.SelectedCandidatePath = assetPathByID(session.Candidates, session.SelectedCandidateID)
	if manifest.StateAssetPaths == nil {
		manifest.StateAssetPaths = map[string]string{}
	}
	if manifest.StateSourceImagePaths == nil {
		manifest.StateSourceImagePaths = map[string]string{}
	}
	if err := writeScriptManifest(filepath.Join(workspaceRoot, "manifest.json"), manifest); err != nil {
		return scriptManifest{}, "", err
	}
	return manifest, workspaceRoot, nil
}

func (g ScriptGenerator) runMainAgentScript(ctx context.Context, sessionID string, env GenerationEnvironment, relativeScriptPath string, args []string) error {
	pythonExecutable := g.PythonExecutable
	if strings.TrimSpace(pythonExecutable) == "" {
		pythonExecutable = "python3"
	}

	dataRoot, err := filepath.Abs(env.DataRoot)
	if err != nil {
		return fmt.Errorf("resolve data root: %w", err)
	}
	workspaceRoot := filepath.Join(dataRoot, sessionID, "workspace")
	repoRoot, err := g.resolveRepoRoot()
	if err != nil {
		return err
	}

	commandArgs := append([]string{"-u", filepath.Join(repoRoot, relativeScriptPath)}, args...)
	command := exec.CommandContext(ctx, pythonExecutable, commandArgs...)
	command.Dir = repoRoot
	command.Env = append(os.Environ(),
		"PYTHONUNBUFFERED=1",
		"TONGUE_REPO_ROOT="+repoRoot,
		"TONGUE_MAIN_AGENT_WORKSPACE_ROOT="+workspaceRoot,
	)
	if strings.TrimSpace(g.ImageGeneratorScript) != "" {
		command.Env = append(command.Env, "TONGUE_MAIN_AGENT_IMAGE_GENERATOR_SCRIPT="+g.ImageGeneratorScript)
	}
	if strings.TrimSpace(g.StatePipelineScript) != "" {
		command.Env = append(command.Env, "TONGUE_SPECIALIST_STATE_PIPELINE_SCRIPT="+g.StatePipelineScript)
	}

	startedAt := time.Now()
	deadline, hasDeadline := ctx.Deadline()
	if hasDeadline {
		fmt.Printf(
			"[minime] running script %s for session %s (deadline=%s, workspace=%s)\n",
			filepath.Base(relativeScriptPath),
			sessionID,
			deadline.Format(time.RFC3339),
			workspaceRoot,
		)
	} else {
		fmt.Printf(
			"[minime] running script %s for session %s (workspace=%s)\n",
			filepath.Base(relativeScriptPath),
			sessionID,
			workspaceRoot,
		)
	}
	output, err := command.CombinedOutput()
	trimmedOutput := strings.TrimSpace(string(output))
	if trimmedOutput != "" {
		fmt.Printf("[minime] output from %s for session %s:\n%s\n", filepath.Base(relativeScriptPath), sessionID, trimmedOutput)
	}
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf(
				"%s timed out or was cancelled after %s: %w\n%s",
				filepath.Base(relativeScriptPath),
				time.Since(startedAt).Round(time.Millisecond),
				ctx.Err(),
				trimmedOutput,
			)
		}
		return fmt.Errorf(
			"%s failed after %s: %w\n%s",
			filepath.Base(relativeScriptPath),
			time.Since(startedAt).Round(time.Millisecond),
			err,
			trimmedOutput,
		)
	}
	fmt.Printf(
		"[minime] finished script %s for session %s in %s\n",
		filepath.Base(relativeScriptPath),
		sessionID,
		time.Since(startedAt).Round(time.Millisecond),
	)
	return nil
}

func (g ScriptGenerator) resolveRepoRoot() (string, error) {
	if trimmed := strings.TrimSpace(g.RepoRoot); trimmed != "" {
		return trimmed, nil
	}

	currentDirectory, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve repo root: %w", err)
	}

	directory := currentDirectory
	for {
		if _, err := os.Stat(filepath.Join(directory, "scripts", "bootstrap_main_agent_creation.py")); err == nil {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", fmt.Errorf("resolve repo root: could not locate scripts/bootstrap_main_agent_creation.py")
		}
		directory = parent
	}
}

func (g ScriptGenerator) readManifest(sessionID string, env GenerationEnvironment) (scriptManifest, string, error) {
	workspaceRoot := filepath.Join(env.DataRoot, sessionID, "workspace")
	manifestPath := filepath.Join(workspaceRoot, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return scriptManifest{}, "", fmt.Errorf("read script manifest: %w", err)
	}

	var manifest scriptManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return scriptManifest{}, "", fmt.Errorf("decode script manifest: %w", err)
	}
	if manifest.StateAssetPaths == nil {
		manifest.StateAssetPaths = map[string]string{}
	}
	if manifest.StateSourceImagePaths == nil {
		manifest.StateSourceImagePaths = map[string]string{}
	}
	return manifest, workspaceRoot, nil
}

func writeScriptManifest(path string, manifest scriptManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal script manifest: %w", err)
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func assetPaths(assets []*assetRecord) []string {
	paths := make([]string, 0, len(assets))
	for _, asset := range assets {
		if asset != nil && asset.FilePath != "" {
			paths = append(paths, asset.FilePath)
		}
	}
	return paths
}

func assetPathByID(assets []*assetRecord, assetID string) string {
	asset := findAssetByID(assets, assetID)
	if asset == nil {
		return ""
	}
	return asset.FilePath
}
