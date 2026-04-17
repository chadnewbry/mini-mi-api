package minime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type tongueSnapshotClient struct {
	baseURL       *url.URL
	internalToken string
	client        *http.Client
}

type tongueSessionSnapshotPayload struct {
	SessionID           string                       `json:"session_id"`
	Status              string                       `json:"status"`
	CurrentStepLabel    string                       `json:"current_step_label,omitempty"`
	CurrentIndex        *int                         `json:"current_index,omitempty"`
	TotalCount          *int                         `json:"total_count,omitempty"`
	Notes               string                       `json:"notes,omitempty"`
	SourcePhotos        []tongueAssetRefPayload      `json:"source_photos,omitempty"`
	Candidates          []tongueAssetRefPayload      `json:"candidates,omitempty"`
	LastJobID           string                       `json:"last_job_id,omitempty"`
	SelectedSourcePhoto string                       `json:"selected_source_photo_id,omitempty"`
	SelectedCandidate   string                       `json:"selected_candidate_id,omitempty"`
	PublishedPreview    *tongueAssetRefPayload       `json:"published_preview_asset,omitempty"`
	StateAssets         []tongueStateAssetRefPayload `json:"state_assets,omitempty"`
	PublishedPreviewID  string                       `json:"published_preview_asset_id,omitempty"`
	PublishedPreviewURL string                       `json:"published_preview_url,omitempty"`
}

type tongueAssetRefPayload struct {
	ID          string `json:"id"`
	Filename    string `json:"filename,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`
}

type tongueStateAssetRefPayload struct {
	StateName   string                 `json:"state_name"`
	SourceImage *tongueAssetRefPayload `json:"source_image,omitempty"`
	FinalAsset  *tongueAssetRefPayload `json:"final_asset,omitempty"`
}

func newTongueSnapshotClient(config Config) (*tongueSnapshotClient, error) {
	rawBaseURL := strings.TrimSpace(config.TongueAPIBaseURL)
	internalToken := strings.TrimSpace(config.TongueInternalToken)
	if rawBaseURL == "" || internalToken == "" {
		return nil, nil
	}

	parsedBaseURL, err := url.Parse(rawBaseURL)
	if err != nil || parsedBaseURL.Scheme == "" || parsedBaseURL.Host == "" {
		return nil, fmt.Errorf("invalid Tongue API base URL %q", rawBaseURL)
	}
	parsedBaseURL.RawQuery = ""
	parsedBaseURL.Fragment = ""

	client := config.TongueAPIHTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}

	return &tongueSnapshotClient{
		baseURL:       parsedBaseURL,
		internalToken: internalToken,
		client:        client,
	}, nil
}

func (c *tongueSnapshotClient) UpsertSessionSnapshot(ctx context.Context, session *sessionRecord) error {
	if c == nil || session == nil {
		return nil
	}

	payload, err := json.Marshal(c.snapshotPayload(session))
	if err != nil {
		return fmt.Errorf("marshal Tongue Mini Me snapshot: %w", err)
	}

	endpoint := *c.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v1/internal/minime/sessions/snapshot"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build Tongue Mini Me snapshot request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Tongue-Internal-Token", c.internalToken)

	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("post Tongue Mini Me snapshot: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}

	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	return fmt.Errorf("post Tongue Mini Me snapshot: status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
}

func (c *tongueSnapshotClient) snapshotPayload(session *sessionRecord) tongueSessionSnapshotPayload {
	payload := tongueSessionSnapshotPayload{
		SessionID:           session.ID,
		Status:              session.Status,
		CurrentStepLabel:    session.CurrentStepLabel,
		CurrentIndex:        cloneSessionInt(session.CurrentIndex),
		TotalCount:          cloneSessionInt(session.TotalCount),
		Notes:               session.Notes,
		SourcePhotos:        c.assetPayloads(session.SourcePhotos),
		Candidates:          c.assetPayloads(session.Candidates),
		LastJobID:           session.LastJobID,
		SelectedSourcePhoto: session.SelectedSourcePhotoID,
		SelectedCandidate:   session.SelectedCandidateID,
		PublishedPreview:    c.assetPayload(session.PublishedPreview),
		StateAssets:         c.stateAssetPayloads(session.StateAssets),
	}
	if payload.PublishedPreview != nil {
		payload.PublishedPreviewID = payload.PublishedPreview.ID
		payload.PublishedPreviewURL = payload.PublishedPreview.DownloadURL
	}
	return payload
}

func (c *tongueSnapshotClient) assetPayloads(values []*assetRecord) []tongueAssetRefPayload {
	if len(values) == 0 {
		return nil
	}

	payloads := make([]tongueAssetRefPayload, 0, len(values))
	for _, value := range values {
		payload := c.assetPayload(value)
		if payload == nil {
			continue
		}
		payloads = append(payloads, *payload)
	}
	if len(payloads) == 0 {
		return nil
	}
	return payloads
}

func (c *tongueSnapshotClient) stateAssetPayloads(values map[string]*stateAssetRecord) []tongueStateAssetRefPayload {
	if len(values) == 0 {
		return nil
	}

	stateNames := make([]string, 0, len(values))
	for stateName := range values {
		stateNames = append(stateNames, stateName)
	}
	sort.Strings(stateNames)

	payloads := make([]tongueStateAssetRefPayload, 0, len(stateNames))
	for _, stateName := range stateNames {
		value := values[stateName]
		if value == nil {
			continue
		}
		payloadStateName := strings.TrimSpace(value.StateName)
		if payloadStateName == "" {
			payloadStateName = stateName
		}
		payloads = append(payloads, tongueStateAssetRefPayload{
			StateName:   payloadStateName,
			SourceImage: c.assetPayload(value.Source),
			FinalAsset:  c.assetPayload(value.Final),
		})
	}
	if len(payloads) == 0 {
		return nil
	}
	return payloads
}

func (c *tongueSnapshotClient) assetPayload(asset *assetRecord) *tongueAssetRefPayload {
	if asset == nil {
		return nil
	}
	return &tongueAssetRefPayload{
		ID:          asset.ID,
		Filename:    asset.FileName,
		DownloadURL: c.assetDownloadURL(asset.ID),
	}
}

func (c *tongueSnapshotClient) assetDownloadURL(assetID string) string {
	if c == nil || c.baseURL == nil {
		return ""
	}
	trimmedAssetID := strings.TrimSpace(assetID)
	if trimmedAssetID == "" {
		return ""
	}

	endpoint := *c.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v1/minime/assets/" + url.PathEscape(trimmedAssetID)
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	return endpoint.String()
}

func cloneSessionInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
