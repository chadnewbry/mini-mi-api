package minime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
)

var ErrNoSelectedAsset = errors.New("select a source photo or candidate before generating states")

type Generator interface {
	Bootstrap(ctx context.Context, env GenerationEnvironment, session *sessionRecord) error
	GenerateCandidates(ctx context.Context, env GenerationEnvironment, session *sessionRecord) error
	GenerateStates(ctx context.Context, env GenerationEnvironment, session *sessionRecord, states []string) error
}

type GenerationEnvironment struct {
	DataRoot   string
	CloneAsset func(sessionID string, source *assetRecord, subdirectory, fileName string) (*assetRecord, error)
	ImportFile func(sessionID, subdirectory, filePath string) (*assetRecord, error)
}

type PlaceholderGenerator struct{}

func (PlaceholderGenerator) Bootstrap(_ context.Context, _ GenerationEnvironment, session *sessionRecord) error {
	session.Status = "workspace-bootstrapped"
	session.CurrentStepLabel = "Workspace bootstrapped"
	session.Notes = "Backend workspace prepared."
	return nil
}

func (PlaceholderGenerator) GenerateCandidates(_ context.Context, env GenerationEnvironment, session *sessionRecord) error {
	session.Candidates = nil
	for index, source := range session.SourcePhotos {
		candidate, err := env.CloneAsset(
			session.ID,
			source,
			"candidate-renders",
			fmt.Sprintf("candidate-%02d%s", index+1, extensionForFile(source.FileName)),
		)
		if err != nil {
			return err
		}
		session.Candidates = append(session.Candidates, candidate)
		current := index + 1
		session.CurrentIndex = &current
	}

	total := len(session.Candidates)
	session.TotalCount = &total
	session.Status = "candidate-generated"
	session.CurrentStepLabel = "Candidates generated"
	session.Notes = "Generated placeholder candidates from uploaded source photos."
	if session.SelectedCandidateID == "" && len(session.Candidates) > 0 {
		session.SelectedCandidateID = session.Candidates[0].ID
		session.PublishedPreview = session.Candidates[0]
	}
	return nil
}

func (PlaceholderGenerator) GenerateStates(_ context.Context, env GenerationEnvironment, session *sessionRecord, states []string) error {
	baseAsset := findAssetByID(session.Candidates, session.SelectedCandidateID)
	if baseAsset == nil {
		baseAsset = findAssetByID(session.SourcePhotos, session.SelectedSourcePhotoID)
	}
	if baseAsset == nil {
		return ErrNoSelectedAsset
	}

	session.StateAssets = map[string]*stateAssetRecord{}
	total := len(states)
	session.TotalCount = &total
	for index, stateName := range states {
		normalizedState := normalizeStateName(stateName)
		sourceAsset, err := env.CloneAsset(
			session.ID,
			baseAsset,
			filepath.Join("state-renders", "main-agent", normalizedState),
			"source.png",
		)
		if err != nil {
			return err
		}

		finalAsset, err := env.CloneAsset(
			session.ID,
			baseAsset,
			filepath.Join("state-renders", "main-agent", normalizedState),
			fmt.Sprintf("main-agent-%s%s", normalizedState, extensionForFile(baseAsset.FileName)),
		)
		if err != nil {
			return err
		}

		session.StateAssets[normalizedState] = &stateAssetRecord{
			StateName: normalizedState,
			Source:    sourceAsset,
			Final:     finalAsset,
		}

		current := index + 1
		session.CurrentIndex = &current
	}

	session.Status = "states-generated"
	session.CurrentStepLabel = "State generation complete"
	session.Notes = "Generated placeholder state assets."
	if session.PublishedPreview == nil {
		session.PublishedPreview = baseAsset
	}
	return nil
}
