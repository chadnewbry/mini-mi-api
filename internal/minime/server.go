package minime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

var defaultStates = []string{"idle-day", "listening", "processing", "working", "waving", "idle-night", "celebrate", "talking", "error"}

const interruptedJobError = "server restarted before job completion"
const activeGenerationError = "wait for the current generation job to finish before changing this session"
const maxUploadPhotoSizeBytes int64 = 20 << 20

var allowedUploadContentTypes = map[string]struct{}{
	"image/png":  {},
	"image/jpeg": {},
	"image/gif":  {},
	"image/heic": {},
	"image/webp": {},
}

var allowedStates = func() map[string]struct{} {
	values := make(map[string]struct{}, len(defaultStates))
	for _, state := range defaultStates {
		values[state] = struct{}{}
	}
	return values
}()

type Config struct {
	Port                 string
	DataRoot             string
	DeviceTokens         []string
	WorkerCount          int
	RunWorkers           bool
	WorkerPollInterval   time.Duration
	Generator            Generator
	GeneratorMode        string
	RepoRoot             string
	ImageGeneratorScript string
	PythonExecutable     string
	StatePipelineScript  string
	JobTimeout           time.Duration
}

func LoadConfig() Config {
	port := strings.TrimSpace(os.Getenv("MINIME_PORT"))
	if port == "" {
		port = strings.TrimSpace(os.Getenv("PORT"))
	}
	if port == "" {
		port = "8088"
	}

	dataRoot := strings.TrimSpace(os.Getenv("MINIME_DATA_ROOT"))
	if dataRoot == "" {
		dataRoot = ".data"
	}

	rawTokens := strings.TrimSpace(os.Getenv("MINIME_DEVICE_TOKENS"))
	tokens := []string{"dev-minime-token"}
	if rawTokens != "" {
		tokens = nil
		for _, token := range strings.Split(rawTokens, ",") {
			trimmed := strings.TrimSpace(token)
			if trimmed != "" {
				tokens = append(tokens, trimmed)
			}
		}
		if len(tokens) == 0 {
			tokens = []string{"dev-minime-token"}
		}
	}

	workerCount := 1
	if rawWorkerCount := strings.TrimSpace(os.Getenv("MINIME_WORKER_COUNT")); rawWorkerCount != "" {
		parsedWorkerCount, err := strconv.Atoi(rawWorkerCount)
		if err == nil && parsedWorkerCount > 0 {
			workerCount = parsedWorkerCount
		}
	}

	runWorkers := true
	if rawRunWorkers := strings.TrimSpace(os.Getenv("MINIME_RUN_WORKERS")); rawRunWorkers != "" {
		runWorkers = rawRunWorkers != "0" && !strings.EqualFold(rawRunWorkers, "false")
	}

	workerPollInterval := 250 * time.Millisecond
	if rawPollInterval := strings.TrimSpace(os.Getenv("MINIME_WORKER_POLL_INTERVAL_MS")); rawPollInterval != "" {
		parsedPollInterval, err := strconv.Atoi(rawPollInterval)
		if err == nil && parsedPollInterval > 0 {
			workerPollInterval = time.Duration(parsedPollInterval) * time.Millisecond
		}
	}

	jobTimeout := 20 * time.Minute
	if rawJobTimeout := strings.TrimSpace(os.Getenv("MINIME_JOB_TIMEOUT_SECONDS")); rawJobTimeout != "" {
		parsedJobTimeout, err := strconv.Atoi(rawJobTimeout)
		if err == nil && parsedJobTimeout > 0 {
			jobTimeout = time.Duration(parsedJobTimeout) * time.Second
		}
	}

	return Config{
		Port:                 port,
		DataRoot:             dataRoot,
		DeviceTokens:         tokens,
		WorkerCount:          workerCount,
		RunWorkers:           runWorkers,
		WorkerPollInterval:   workerPollInterval,
		GeneratorMode:        strings.TrimSpace(os.Getenv("MINIME_GENERATOR_MODE")),
		RepoRoot:             strings.TrimSpace(os.Getenv("MINIME_REPO_ROOT")),
		ImageGeneratorScript: strings.TrimSpace(os.Getenv("MINIME_IMAGE_GENERATOR_SCRIPT")),
		PythonExecutable:     strings.TrimSpace(os.Getenv("MINIME_PYTHON_EXECUTABLE")),
		StatePipelineScript:  strings.TrimSpace(os.Getenv("MINIME_STATE_PIPELINE_SCRIPT")),
		JobTimeout:           jobTimeout,
	}
}

type Server struct {
	config Config
	mux    *http.ServeMux

	generator    Generator
	workQueue    chan string
	mu           sync.Mutex
	sessions     map[string]*sessionRecord
	assets       map[string]*assetRecord
	jobs         map[string]*jobRecord
	queuedJobIDs map[string]struct{}
}

type assetRecord struct {
	ID          string `json:"id"`
	SessionID   string `json:"session_id"`
	FileName    string `json:"file_name"`
	ContentType string `json:"content_type"`
	FilePath    string `json:"file_path"`
}

type stateAssetRecord struct {
	StateName string       `json:"state_name"`
	Source    *assetRecord `json:"source,omitempty"`
	Final     *assetRecord `json:"final,omitempty"`
}

type sessionRecord struct {
	ID                    string                       `json:"id"`
	CreatedAt             time.Time                    `json:"created_at"`
	UpdatedAt             time.Time                    `json:"updated_at"`
	Status                string                       `json:"status"`
	CurrentStepLabel      string                       `json:"current_step_label,omitempty"`
	CurrentIndex          *int                         `json:"current_index,omitempty"`
	TotalCount            *int                         `json:"total_count,omitempty"`
	Notes                 string                       `json:"notes,omitempty"`
	SourcePhotos          []*assetRecord               `json:"source_photos,omitempty"`
	Candidates            []*assetRecord               `json:"candidates,omitempty"`
	SelectedSourcePhotoID string                       `json:"selected_source_photo_id,omitempty"`
	SelectedCandidateID   string                       `json:"selected_candidate_id,omitempty"`
	PublishedPreview      *assetRecord                 `json:"published_preview,omitempty"`
	StateAssets           map[string]*stateAssetRecord `json:"state_assets,omitempty"`
	LastJobID             string                       `json:"last_job_id,omitempty"`
}

type jobRecord struct {
	ID              string    `json:"id"`
	SessionID       string    `json:"session_id"`
	Type            string    `json:"type"`
	Status          string    `json:"status"`
	RequestedStates []string  `json:"requested_states,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	FinishedAt      time.Time `json:"finished_at,omitempty"`
	Summary         string    `json:"summary,omitempty"`
	Error           string    `json:"error,omitempty"`
}

type persistedStore struct {
	Sessions []*sessionRecord `json:"sessions"`
	Assets   []*assetRecord   `json:"assets"`
	Jobs     []*jobRecord     `json:"jobs"`
}

type remoteAssetRecord struct {
	ID          string `json:"id"`
	Filename    string `json:"filename,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`
}

type remoteStateAssetRecord struct {
	StateName   string             `json:"state_name"`
	SourceImage *remoteAssetRecord `json:"source_image,omitempty"`
	FinalAsset  *remoteAssetRecord `json:"final_asset,omitempty"`
}

type remoteSessionSnapshot struct {
	SessionID           string                   `json:"session_id"`
	CreatedAt           time.Time                `json:"created_at"`
	UpdatedAt           time.Time                `json:"updated_at"`
	Status              string                   `json:"status"`
	CurrentStepLabel    string                   `json:"current_step_label,omitempty"`
	CurrentIndex        *int                     `json:"current_index,omitempty"`
	TotalCount          *int                     `json:"total_count,omitempty"`
	Notes               string                   `json:"notes,omitempty"`
	SourcePhotos        []remoteAssetRecord      `json:"source_photos"`
	Candidates          []remoteAssetRecord      `json:"candidates"`
	SelectedSourcePhoto string                   `json:"selected_source_photo_id,omitempty"`
	SelectedCandidate   string                   `json:"selected_candidate_id,omitempty"`
	PublishedPreview    *remoteAssetRecord       `json:"published_preview_asset,omitempty"`
	StateAssets         []remoteStateAssetRecord `json:"state_assets"`
	LastJobID           string                   `json:"last_job_id,omitempty"`
}

type remoteJobSnapshot struct {
	JobID      string    `json:"job_id"`
	SessionID  string    `json:"session_id"`
	Type       string    `json:"type"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Summary    string    `json:"summary,omitempty"`
	Error      string    `json:"error,omitempty"`
}

type selectionRequest struct {
	SelectedSourcePhotoID string `json:"selected_source_photo_id"`
	SelectedCandidateID   string `json:"selected_candidate_id"`
}

type statesGenerateRequest struct {
	States       []string `json:"states"`
	PromptSuffix string   `json:"prompt_suffix"`
}

func NewServer(config Config) (*Server, error) {
	if !config.RunWorkers && config.WorkerPollInterval == 0 {
		config.RunWorkers = true
	}
	if config.WorkerCount <= 0 {
		config.WorkerCount = 1
	}
	if config.WorkerPollInterval <= 0 {
		config.WorkerPollInterval = 250 * time.Millisecond
	}
	if err := os.MkdirAll(config.DataRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create data root: %w", err)
	}

	generator, err := generatorForConfig(config)
	if err != nil {
		return nil, err
	}

	server := &Server{
		config:       config,
		mux:          http.NewServeMux(),
		generator:    generator,
		workQueue:    make(chan string, 64),
		sessions:     map[string]*sessionRecord{},
		assets:       map[string]*assetRecord{},
		jobs:         map[string]*jobRecord{},
		queuedJobIDs: map[string]struct{}{},
	}
	if err := server.loadPersistedStore(); err != nil {
		return nil, err
	}
	server.routes()
	if config.RunWorkers {
		server.startWorkers()
	}
	return server, nil
}

func generatorForConfig(config Config) (Generator, error) {
	if config.Generator != nil {
		return config.Generator, nil
	}

	switch strings.TrimSpace(strings.ToLower(config.GeneratorMode)) {
	case "", "placeholder":
		return PlaceholderGenerator{}, nil
	case "script":
		return ScriptGenerator{
			RepoRoot:             config.RepoRoot,
			PythonExecutable:     config.PythonExecutable,
			ImageGeneratorScript: config.ImageGeneratorScript,
			StatePipelineScript:  config.StatePipelineScript,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported generator mode %q", config.GeneratorMode)
	}
}

func (s *Server) Handler() http.Handler {
	protected := s.authMiddleware(s.mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			s.handleHealth(w, r)
			return
		}
		protected.ServeHTTP(w, r)
	})
}

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", s.handleHealth)
	s.mux.HandleFunc("/v1/minime/sessions", s.handleSessions)
	s.mux.HandleFunc("/v1/minime/sessions/", s.handleSessionRoutes)
	s.mux.HandleFunc("/v1/minime/jobs/", s.handleJobRoutes)
	s.mux.HandleFunc("/v1/minime/assets/", s.handleAssetDownload)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := strings.TrimSpace(r.Header.Get("Authorization"))
		if !strings.HasPrefix(header, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}

		token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		if !slices.Contains(s.config.DeviceTokens, token) {
			writeError(w, http.StatusUnauthorized, "invalid device token")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	session := &sessionRecord{
		ID:          newID("session"),
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
		Status:      "created",
		Notes:       "Session created.",
		StateAssets: map[string]*stateAssetRecord{},
	}

	s.mu.Lock()
	if err := s.syncStoreLocked(); err != nil {
		s.mu.Unlock()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.sessions[session.ID] = session
	if err := s.persistStoreLocked(); err != nil {
		s.mu.Unlock()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	snapshot := s.snapshotForSession(r, session)
	s.mu.Unlock()

	writeJSON(w, http.StatusCreated, snapshot)
}

func (s *Server) handleSessionRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/minime/sessions/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	sessionID := parts[0]
	session, err := s.lookupSession(sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeJSON(w, http.StatusOK, s.snapshotForSession(r, session))
		return
	}

	switch parts[1] {
	case "photos":
		s.handlePhotosUpload(w, r, session)
	case "bootstrap":
		s.handleBootstrap(w, r, session)
	case "candidates:generate":
		s.handleGenerateCandidates(w, r, session)
	case "candidate-selection":
		s.handleCandidateSelection(w, r, session)
	case "states:generate":
		s.handleGenerateStates(w, r, session)
	default:
		writeError(w, http.StatusNotFound, "unknown session action")
	}
}

func (s *Server) handleJobRoutes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	jobID := strings.TrimPrefix(r.URL.Path, "/v1/minime/jobs/")
	if jobID == "" {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	s.mu.Lock()
	if err := s.syncStoreLocked(); err != nil {
		s.mu.Unlock()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	job := s.jobs[jobID]
	s.mu.Unlock()
	if job == nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	writeJSON(w, http.StatusOK, s.snapshotForJob(job))
}

func (s *Server) handlePhotosUpload(w http.ResponseWriter, r *http.Request, session *sessionRecord) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}

	files := r.MultipartForm.File["photos"]
	if len(files) == 0 {
		writeError(w, http.StatusBadRequest, "no photos uploaded")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.syncStoreLocked(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	session = s.sessions[session.ID]
	if session == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if sessionHasActiveGenerationJobLocked(s, session) {
		writeError(w, http.StatusConflict, activeGenerationError)
		return
	}

	for _, header := range files {
		if err := validateUploadedPhoto(header); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		file, err := header.Open()
		if err != nil {
			writeError(w, http.StatusBadRequest, "could not open uploaded file")
			return
		}

		asset, err := s.persistUploadedFileLocked(session.ID, "source-photos", header.Filename, header.Header.Get("Content-Type"), file)
		file.Close()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		session.SourcePhotos = append(session.SourcePhotos, asset)
		if session.SelectedSourcePhotoID == "" {
			session.SelectedSourcePhotoID = asset.ID
			session.PublishedPreview = asset
		}
	}

	session.Status = "source-photos-imported"
	session.CurrentStepLabel = "Uploaded source photos"
	session.Notes = "Uploaded source photos."
	session.UpdatedAt = time.Now().UTC()
	job := s.createCompletedJobLocked(session, "photos-upload", "Imported source photos.")

	if err := s.persistStoreLocked(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSONWithHeader(w, http.StatusOK, s.snapshotForSession(r, session), map[string]string{
		"X-MiniMe-Job-ID": job.ID,
	})
}

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request, session *sessionRecord) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	s.mu.Lock()
	if err := s.syncStoreLocked(); err != nil {
		s.mu.Unlock()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	session = s.sessions[session.ID]
	if session == nil {
		s.mu.Unlock()
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if sessionHasActiveGenerationJobLocked(s, session) {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, activeGenerationError)
		return
	}
	workingSession := cloneSessionRecord(session)
	s.mu.Unlock()

	if err := s.generator.Bootstrap(context.Background(), s.generationEnvironment(), workingSession); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	session = s.sessions[workingSession.ID]
	if session == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	session.Status = workingSession.Status
	session.CurrentStepLabel = workingSession.CurrentStepLabel
	session.Notes = workingSession.Notes
	session.CurrentIndex = workingSession.CurrentIndex
	session.TotalCount = workingSession.TotalCount
	session.UpdatedAt = time.Now().UTC()
	job := s.createCompletedJobLocked(session, "bootstrap", "Workspace bootstrapped.")

	if err := s.persistStoreLocked(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSONWithHeader(w, http.StatusOK, s.snapshotForSession(r, session), map[string]string{
		"X-MiniMe-Job-ID": job.ID,
	})
}

func (s *Server) handleGenerateCandidates(w http.ResponseWriter, r *http.Request, session *sessionRecord) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	s.mu.Lock()
	if sessionHasActiveGenerationJobLocked(s, session) {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, activeGenerationError)
		return
	}
	if err := s.syncStoreLocked(); err != nil {
		s.mu.Unlock()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	session = s.sessions[session.ID]
	if session == nil {
		s.mu.Unlock()
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if len(session.SourcePhotos) == 0 {
		s.mu.Unlock()
		writeError(w, http.StatusBadRequest, "import source photos before generating candidates")
		return
	}

	job := s.createQueuedJobLocked(session, "generate-candidates", nil)
	session.Status = "queued-candidates"
	session.CurrentStepLabel = "Queued candidate generation"
	session.Notes = "Candidate generation queued."
	session.UpdatedAt = time.Now().UTC()
	if err := s.persistStoreLocked(); err != nil {
		s.mu.Unlock()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	snapshot := s.snapshotForSession(r, session)
	s.mu.Unlock()
	s.enqueueJob(job.ID)

	writeJSONWithHeader(w, http.StatusOK, snapshot, map[string]string{
		"X-MiniMe-Job-ID": job.ID,
	})
}

func (s *Server) handleCandidateSelection(w http.ResponseWriter, r *http.Request, session *sessionRecord) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var request selectionRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid selection payload")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.syncStoreLocked(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	session = s.sessions[session.ID]
	if session == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if sessionHasActiveGenerationJobLocked(s, session) {
		writeError(w, http.StatusConflict, activeGenerationError)
		return
	}

	if request.SelectedSourcePhotoID != "" {
		asset := findAssetByID(session.SourcePhotos, request.SelectedSourcePhotoID)
		if asset == nil {
			writeError(w, http.StatusBadRequest, "selected source photo was not found in this session")
			return
		}
		session.SelectedSourcePhotoID = asset.ID
		if session.SelectedCandidateID == "" {
			session.PublishedPreview = asset
		}
	}

	if request.SelectedCandidateID != "" {
		asset := findAssetByID(session.Candidates, request.SelectedCandidateID)
		if asset == nil {
			writeError(w, http.StatusBadRequest, "selected candidate was not found in this session")
			return
		}
		session.SelectedCandidateID = asset.ID
		session.PublishedPreview = asset
	}

	session.Status = "candidate-selected"
	session.CurrentStepLabel = "Selection updated"
	session.Notes = "Updated selected Mini Me assets."
	session.UpdatedAt = time.Now().UTC()
	job := s.createCompletedJobLocked(session, "candidate-selection", "Updated selected Mini Me assets.")

	if err := s.persistStoreLocked(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSONWithHeader(w, http.StatusOK, s.snapshotForSession(r, session), map[string]string{
		"X-MiniMe-Job-ID": job.ID,
	})
}

func (s *Server) handleGenerateStates(w http.ResponseWriter, r *http.Request, session *sessionRecord) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var request statesGenerateRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&request)
	}

	s.mu.Lock()
	if err := s.syncStoreLocked(); err != nil {
		s.mu.Unlock()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	session = s.sessions[session.ID]
	if session == nil {
		s.mu.Unlock()
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if sessionHasActiveGenerationJobLocked(s, session) {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, activeGenerationError)
		return
	}

	states := request.States
	if len(states) == 0 {
		states = defaultStates
	}
	normalizedStates, err := normalizeRequestedStates(states)
	if err != nil {
		s.mu.Unlock()
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !sessionHasSelectedAsset(session) {
		job := s.createFailedJobLocked(session, "generate-states", normalizedStates, ErrNoSelectedAsset)
		session.Status = "failed"
		session.CurrentStepLabel = "Generation failed"
		session.Notes = ErrNoSelectedAsset.Error()
		session.UpdatedAt = job.UpdatedAt
		if persistErr := s.persistStoreLocked(); persistErr != nil {
			s.mu.Unlock()
			writeError(w, http.StatusInternalServerError, persistErr.Error())
			return
		}
		s.mu.Unlock()
		writeError(w, http.StatusBadRequest, ErrNoSelectedAsset.Error())
		return
	}
	job := s.createQueuedJobLocked(session, "generate-states", normalizedStates)
	session.Status = "queued-states"
	session.CurrentStepLabel = "Queued state generation"
	session.Notes = "State generation queued."
	session.UpdatedAt = time.Now().UTC()
	if err := s.persistStoreLocked(); err != nil {
		s.mu.Unlock()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	snapshot := s.snapshotForSession(r, session)
	s.mu.Unlock()
	s.enqueueJob(job.ID)

	writeJSONWithHeader(w, http.StatusOK, snapshot, map[string]string{
		"X-MiniMe-Job-ID": job.ID,
	})
}

func (s *Server) handleAssetDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	assetID := strings.TrimPrefix(r.URL.Path, "/v1/minime/assets/")
	if assetID == "" {
		writeError(w, http.StatusNotFound, "asset not found")
		return
	}

	s.mu.Lock()
	if err := s.syncStoreLocked(); err != nil {
		s.mu.Unlock()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	asset := s.assets[assetID]
	s.mu.Unlock()
	if asset == nil {
		writeError(w, http.StatusNotFound, "asset not found")
		return
	}

	if asset.ContentType != "" {
		w.Header().Set("Content-Type", asset.ContentType)
	} else {
		w.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(asset.FileName)))
	}
	http.ServeFile(w, r, asset.FilePath)
}

func (s *Server) lookupSession(sessionID string) (*sessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.syncStoreLocked(); err != nil {
		return nil, err
	}

	session := s.sessions[sessionID]
	if session == nil {
		return nil, errors.New("session not found")
	}
	return session, nil
}

func (s *Server) persistUploadedFileLocked(sessionID, subdirectory, fileName, contentType string, reader io.Reader) (*assetRecord, error) {
	asset := &assetRecord{
		ID:          newID("asset"),
		SessionID:   sessionID,
		FileName:    sanitizeFileName(fileName),
		ContentType: normalizeContentType(contentType, fileName),
	}
	if asset.FileName == "" {
		asset.FileName = "upload.bin"
	}

	directory := filepath.Join(s.config.DataRoot, sessionID, subdirectory)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, fmt.Errorf("create asset directory: %w", err)
	}

	filePath := filepath.Join(directory, asset.ID+"-"+asset.FileName)
	file, err := os.Create(filePath)
	if err != nil {
		return nil, fmt.Errorf("create asset file: %w", err)
	}
	defer file.Close()

	if _, err := io.Copy(file, reader); err != nil {
		return nil, fmt.Errorf("write asset file: %w", err)
	}

	asset.FilePath = filePath
	s.assets[asset.ID] = asset
	return asset, nil
}

func (s *Server) cloneAssetLocked(sessionID string, source *assetRecord, subdirectory, fileName string) (*assetRecord, error) {
	input, err := os.Open(source.FilePath)
	if err != nil {
		return nil, fmt.Errorf("open source asset: %w", err)
	}
	defer input.Close()

	return s.persistUploadedFileLocked(sessionID, subdirectory, fileName, source.ContentType, input)
}

func (s *Server) generationEnvironment() GenerationEnvironment {
	return GenerationEnvironment{
		DataRoot: s.config.DataRoot,
		CloneAsset: func(sessionID string, source *assetRecord, subdirectory, fileName string) (*assetRecord, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if err := s.syncStoreLocked(); err != nil {
				return nil, err
			}
			asset, err := s.cloneAssetLocked(sessionID, source, subdirectory, fileName)
			if err != nil {
				return nil, err
			}
			if err := s.persistStoreLocked(); err != nil {
				return nil, err
			}
			return asset, nil
		},
		ImportFile: func(sessionID, subdirectory, filePath string) (*assetRecord, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if err := s.syncStoreLocked(); err != nil {
				return nil, err
			}
			asset, err := s.importFileLocked(sessionID, subdirectory, filePath)
			if err != nil {
				return nil, err
			}
			if err := s.persistStoreLocked(); err != nil {
				return nil, err
			}
			return asset, nil
		},
	}
}

func (s *Server) importFileLocked(sessionID, subdirectory, filePath string) (*assetRecord, error) {
	input, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open generated asset: %w", err)
	}
	defer input.Close()

	fileName := filepath.Base(filePath)
	contentType := normalizeContentType("", fileName)
	return s.persistUploadedFileLocked(sessionID, subdirectory, fileName, contentType, input)
}

func (s *Server) createQueuedJobLocked(session *sessionRecord, jobType string, requestedStates []string) *jobRecord {
	now := time.Now().UTC()
	job := &jobRecord{
		ID:              newID("job"),
		SessionID:       session.ID,
		Type:            jobType,
		Status:          "queued",
		RequestedStates: append([]string(nil), requestedStates...),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	s.jobs[job.ID] = job
	session.LastJobID = job.ID
	return job
}

func (s *Server) createCompletedJobLocked(session *sessionRecord, jobType, summary string) *jobRecord {
	job := &jobRecord{
		ID:        newID("job"),
		SessionID: session.ID,
		Type:      jobType,
		Status:    "completed",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	s.jobs[job.ID] = job
	session.LastJobID = job.ID
	s.completeJobLocked(job, summary)
	return job
}

func (s *Server) createFailedJobLocked(session *sessionRecord, jobType string, requestedStates []string, err error) *jobRecord {
	job := &jobRecord{
		ID:              newID("job"),
		SessionID:       session.ID,
		Type:            jobType,
		Status:          "failed",
		RequestedStates: append([]string(nil), requestedStates...),
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}
	s.jobs[job.ID] = job
	session.LastJobID = job.ID
	s.failJobLocked(job, err)
	return job
}

func (s *Server) completeJobLocked(job *jobRecord, summary string) {
	now := time.Now().UTC()
	job.Status = "completed"
	job.UpdatedAt = now
	job.FinishedAt = now
	job.Summary = summary
	job.Error = ""
}

func (s *Server) failJobLocked(job *jobRecord, err error) {
	now := time.Now().UTC()
	job.Status = "failed"
	job.UpdatedAt = now
	job.FinishedAt = now
	job.Error = err.Error()
}

func (s *Server) snapshotForSession(r *http.Request, session *sessionRecord) remoteSessionSnapshot {
	sourcePhotos := make([]remoteAssetRecord, 0, len(session.SourcePhotos))
	for _, asset := range session.SourcePhotos {
		sourcePhotos = append(sourcePhotos, s.remoteAssetRecord(r, asset))
	}

	candidates := make([]remoteAssetRecord, 0, len(session.Candidates))
	for _, asset := range session.Candidates {
		candidates = append(candidates, s.remoteAssetRecord(r, asset))
	}

	stateNames := make([]string, 0, len(session.StateAssets))
	for stateName := range session.StateAssets {
		stateNames = append(stateNames, stateName)
	}
	slices.Sort(stateNames)

	stateAssets := make([]remoteStateAssetRecord, 0, len(stateNames))
	for _, stateName := range stateNames {
		record := session.StateAssets[stateName]
		stateAssets = append(stateAssets, remoteStateAssetRecord{
			StateName:   record.StateName,
			SourceImage: optionalRemoteAssetRecord(s.remoteAssetRecord(r, record.Source)),
			FinalAsset:  optionalRemoteAssetRecord(s.remoteAssetRecord(r, record.Final)),
		})
	}

	return remoteSessionSnapshot{
		SessionID:           session.ID,
		CreatedAt:           session.CreatedAt,
		UpdatedAt:           session.UpdatedAt,
		Status:              session.Status,
		CurrentStepLabel:    session.CurrentStepLabel,
		CurrentIndex:        session.CurrentIndex,
		TotalCount:          session.TotalCount,
		Notes:               session.Notes,
		SourcePhotos:        sourcePhotos,
		Candidates:          candidates,
		SelectedSourcePhoto: session.SelectedSourcePhotoID,
		SelectedCandidate:   session.SelectedCandidateID,
		PublishedPreview:    optionalRemoteAssetRecord(s.remoteAssetRecord(r, session.PublishedPreview)),
		StateAssets:         stateAssets,
		LastJobID:           session.LastJobID,
	}
}

func (s *Server) snapshotForJob(job *jobRecord) remoteJobSnapshot {
	return remoteJobSnapshot{
		JobID:      job.ID,
		SessionID:  job.SessionID,
		Type:       job.Type,
		Status:     job.Status,
		CreatedAt:  job.CreatedAt,
		UpdatedAt:  job.UpdatedAt,
		FinishedAt: job.FinishedAt,
		Summary:    job.Summary,
		Error:      job.Error,
	}
}

func (s *Server) startWorkers() {
	for workerIndex := 0; workerIndex < s.config.WorkerCount; workerIndex++ {
		go s.runWorker()
	}
	go s.pollForQueuedJobs()
}

func (s *Server) runWorker() {
	for jobID := range s.workQueue {
		s.processJob(jobID)
	}
}

func (s *Server) enqueueJob(jobID string) {
	s.mu.Lock()
	if _, exists := s.queuedJobIDs[jobID]; exists {
		s.mu.Unlock()
		return
	}
	s.queuedJobIDs[jobID] = struct{}{}
	s.mu.Unlock()
	s.workQueue <- jobID
}

func (s *Server) pollForQueuedJobs() {
	s.scanQueuedJobs()
	ticker := time.NewTicker(s.config.WorkerPollInterval)
	defer ticker.Stop()

	for range ticker.C {
		s.scanQueuedJobs()
	}
}

func (s *Server) scanQueuedJobs() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.syncStoreLocked(); err != nil {
		return
	}
	for _, job := range s.jobs {
		if job.Status != "queued" {
			delete(s.queuedJobIDs, job.ID)
			continue
		}
		if _, exists := s.queuedJobIDs[job.ID]; exists {
			continue
		}
		s.queuedJobIDs[job.ID] = struct{}{}
		s.workQueue <- job.ID
	}
}

func (s *Server) processJob(jobID string) {
	s.mu.Lock()
	defer delete(s.queuedJobIDs, jobID)
	if err := s.syncStoreLocked(); err != nil {
		s.mu.Unlock()
		return
	}
	job := s.jobs[jobID]
	if job == nil || job.Status != "queued" {
		s.mu.Unlock()
		return
	}

	session := s.sessions[job.SessionID]
	if session == nil {
		s.failJobLocked(job, errors.New("session not found"))
		_ = s.persistStoreLocked()
		s.mu.Unlock()
		return
	}

	workingSession := cloneSessionRecord(session)
	jobType := job.Type
	requestedStates := append([]string(nil), job.RequestedStates...)
	job.Status = "running"
	job.UpdatedAt = time.Now().UTC()
	switch jobType {
	case "generate-candidates":
		session.Status = "generating-candidates"
		session.CurrentStepLabel = "Generating candidates"
		session.Notes = "Generating placeholder candidates from uploaded source photos."
	case "generate-states":
		session.Status = "generating-states"
		session.CurrentStepLabel = "Generating states"
		session.Notes = "Generating placeholder state assets."
	}
	session.UpdatedAt = job.UpdatedAt
	if err := s.persistStoreLocked(); err != nil {
		s.failJobLocked(job, err)
		session.Status = "failed"
		session.CurrentStepLabel = "Generation failed"
		session.Notes = err.Error()
		_ = s.persistStoreLocked()
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	var err error
	jobContext := context.Background()
	cancel := func() {}
	if s.config.JobTimeout > 0 {
		jobContext, cancel = context.WithTimeout(context.Background(), s.config.JobTimeout)
	}
	defer cancel()
	switch jobType {
	case "generate-candidates":
		err = s.generator.GenerateCandidates(jobContext, s.generationEnvironment(), workingSession)
	case "generate-states":
		err = s.generator.GenerateStates(jobContext, s.generationEnvironment(), workingSession, requestedStates)
	default:
		err = fmt.Errorf("unsupported queued job type %q", jobType)
	}

	if errors.Is(jobContext.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("%s job exceeded %s", jobType, s.config.JobTimeout)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.syncStoreLocked(); err != nil {
		return
	}
	job = s.jobs[jobID]
	if job == nil {
		return
	}
	session = s.sessions[job.SessionID]
	if session == nil {
		s.failJobLocked(job, errors.New("session not found"))
		_ = s.persistStoreLocked()
		return
	}
	if job.Status != "running" {
		return
	}

	if err != nil {
		s.failJobLocked(job, err)
		session.Status = "failed"
		session.CurrentStepLabel = "Generation failed"
		session.Notes = err.Error()
		session.UpdatedAt = job.UpdatedAt
		_ = s.persistStoreLocked()
		return
	}

	workingSession.LastJobID = job.ID
	workingSession.UpdatedAt = time.Now().UTC()
	s.sessions[job.SessionID] = workingSession
	switch jobType {
	case "generate-candidates":
		s.completeJobLocked(job, "Candidates generated.")
	case "generate-states":
		s.completeJobLocked(job, "State generation complete.")
	default:
		s.completeJobLocked(job, "Job complete.")
	}
	workingSession.UpdatedAt = job.UpdatedAt
	_ = s.persistStoreLocked()
}

func (s *Server) remoteAssetRecord(r *http.Request, asset *assetRecord) remoteAssetRecord {
	if asset == nil {
		return remoteAssetRecord{}
	}
	return remoteAssetRecord{
		ID:          asset.ID,
		Filename:    asset.FileName,
		DownloadURL: absoluteURL(r, "/v1/minime/assets/"+asset.ID),
	}
}

func (s *Server) persistStoreLocked() error {
	store := persistedStore{
		Sessions: make([]*sessionRecord, 0, len(s.sessions)),
		Assets:   make([]*assetRecord, 0, len(s.assets)),
		Jobs:     make([]*jobRecord, 0, len(s.jobs)),
	}
	for _, session := range s.sessions {
		store.Sessions = append(store.Sessions, session)
	}
	for _, asset := range s.assets {
		store.Assets = append(store.Assets, asset)
	}
	for _, job := range s.jobs {
		store.Jobs = append(store.Jobs, job)
	}

	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal store: %w", err)
	}

	tempPath := s.storePath() + ".tmp"
	if err := os.WriteFile(tempPath, data, 0o644); err != nil {
		return fmt.Errorf("write store temp file: %w", err)
	}
	if err := os.Rename(tempPath, s.storePath()); err != nil {
		return fmt.Errorf("replace store file: %w", err)
	}
	return nil
}

func (s *Server) loadPersistedStore() error {
	if err := s.syncStoreLocked(); err != nil {
		return err
	}
	if s.config.RunWorkers {
		s.recoverInterruptedJobs()
		if err := s.persistStoreLocked(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) syncStoreLocked() error {
	path := s.storePath()
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		s.sessions = map[string]*sessionRecord{}
		s.assets = map[string]*assetRecord{}
		s.jobs = map[string]*jobRecord{}
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read store file: %w", err)
	}

	var store persistedStore
	if err := json.Unmarshal(data, &store); err != nil {
		return fmt.Errorf("decode store file: %w", err)
	}

	s.sessions = map[string]*sessionRecord{}
	s.assets = map[string]*assetRecord{}
	s.jobs = map[string]*jobRecord{}
	for _, session := range store.Sessions {
		if session.StateAssets == nil {
			session.StateAssets = map[string]*stateAssetRecord{}
		}
		s.sessions[session.ID] = session
	}
	for _, asset := range store.Assets {
		s.assets[asset.ID] = asset
	}
	for _, job := range store.Jobs {
		s.jobs[job.ID] = job
	}
	return nil
}

func (s *Server) recoverInterruptedJobs() {
	for _, job := range s.jobs {
		switch job.Status {
		case "running":
			s.failJobLocked(job, errors.New(interruptedJobError))
			if session := s.sessions[job.SessionID]; session != nil {
				session.Status = "failed"
				session.CurrentStepLabel = "Generation interrupted"
				session.Notes = interruptedJobError
				session.UpdatedAt = job.UpdatedAt
				session.LastJobID = job.ID
			}
		case "queued":
			session := s.sessions[job.SessionID]
			if session == nil {
				s.failJobLocked(job, errors.New("session not found"))
				continue
			}
			session.LastJobID = job.ID
			s.enqueueJob(job.ID)
		}
	}
}

func (s *Server) storePath() string {
	return filepath.Join(s.config.DataRoot, "store.json")
}

func absoluteURL(r *http.Request, path string) string {
	scheme := "http"
	if forwardedProto := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); forwardedProto != "" {
		scheme = forwardedProto
	} else if r.TLS != nil {
		scheme = "https"
	}

	host := r.Host
	if forwardedHost := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); forwardedHost != "" {
		host = forwardedHost
	}

	return scheme + "://" + host + path
}

func optionalRemoteAssetRecord(record remoteAssetRecord) *remoteAssetRecord {
	if record.ID == "" {
		return nil
	}
	return &record
}

func findAssetByID(assets []*assetRecord, id string) *assetRecord {
	for _, asset := range assets {
		if asset.ID == id {
			return asset
		}
	}
	return nil
}

func normalizeStateName(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "idle":
		return "idle-day"
	case "sleeping":
		return "idle-night"
	default:
		return strings.TrimSpace(strings.ToLower(value))
	}
}

func normalizeRequestedStates(states []string) ([]string, error) {
	normalized := make([]string, 0, len(states))
	seen := make(map[string]struct{}, len(states))
	for _, state := range states {
		normalizedState := normalizeStateName(state)
		if normalizedState == "" {
			return nil, errors.New("state names must not be empty")
		}
		if _, allowed := allowedStates[normalizedState]; !allowed {
			return nil, fmt.Errorf("unsupported state %q", state)
		}
		if _, exists := seen[normalizedState]; exists {
			continue
		}
		seen[normalizedState] = struct{}{}
		normalized = append(normalized, normalizedState)
	}
	return normalized, nil
}

func cloneSessionRecord(session *sessionRecord) *sessionRecord {
	if session == nil {
		return nil
	}

	cloned := *session
	cloned.SourcePhotos = append([]*assetRecord(nil), session.SourcePhotos...)
	cloned.Candidates = append([]*assetRecord(nil), session.Candidates...)
	cloned.StateAssets = make(map[string]*stateAssetRecord, len(session.StateAssets))
	for stateName, asset := range session.StateAssets {
		if asset == nil {
			cloned.StateAssets[stateName] = nil
			continue
		}
		copiedAsset := *asset
		cloned.StateAssets[stateName] = &copiedAsset
	}
	if session.CurrentIndex != nil {
		currentIndex := *session.CurrentIndex
		cloned.CurrentIndex = &currentIndex
	}
	if session.TotalCount != nil {
		totalCount := *session.TotalCount
		cloned.TotalCount = &totalCount
	}
	return &cloned
}

func sessionHasActiveGenerationJobLocked(server *Server, session *sessionRecord) bool {
	if server == nil || session == nil {
		return false
	}
	for _, job := range server.jobs {
		if job.SessionID != session.ID {
			continue
		}
		if job.Type != "generate-candidates" && job.Type != "generate-states" {
			continue
		}
		if job.Status == "queued" || job.Status == "running" {
			return true
		}
	}
	return false
}

func sessionHasSelectedAsset(session *sessionRecord) bool {
	if session == nil {
		return false
	}
	return findAssetByID(session.Candidates, session.SelectedCandidateID) != nil ||
		findAssetByID(session.SourcePhotos, session.SelectedSourcePhotoID) != nil
}

func normalizeContentType(contentType, fileName string) string {
	if strings.TrimSpace(contentType) != "" {
		return contentType
	}
	if guessed := mime.TypeByExtension(filepath.Ext(fileName)); guessed != "" {
		return guessed
	}
	return "application/octet-stream"
}

func validateUploadedPhoto(header *multipart.FileHeader) error {
	if header == nil {
		return errors.New("missing uploaded photo")
	}
	if header.Size <= 0 {
		return errors.New("uploaded photo is empty")
	}
	if header.Size > maxUploadPhotoSizeBytes {
		return fmt.Errorf("uploaded photo exceeds %d MB limit", maxUploadPhotoSizeBytes>>20)
	}

	contentType := normalizeContentType(header.Header.Get("Content-Type"), header.Filename)
	if contentType == "application/octet-stream" {
		if inferred := mime.TypeByExtension(filepath.Ext(header.Filename)); inferred != "" {
			contentType = inferred
		}
	}
	if _, allowed := allowedUploadContentTypes[contentType]; !allowed {
		return fmt.Errorf("unsupported uploaded photo content type %q", contentType)
	}

	return nil
}

func sanitizeFileName(value string) string {
	name := strings.TrimSpace(filepath.Base(value))
	name = strings.ReplaceAll(name, " ", "-")
	return name
}

func extensionForFile(fileName string) string {
	extension := filepath.Ext(fileName)
	if extension == "" {
		return ".png"
	}
	return extension
}

func newID(prefix string) string {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(bytes)
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	writeJSONWithHeader(w, statusCode, payload, nil)
}

func writeJSONWithHeader(w http.ResponseWriter, statusCode int, payload any, headers map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	for key, value := range headers {
		w.Header().Set(key, value)
	}
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, statusCode int, message string) {
	writeJSON(w, statusCode, map[string]string{"error": message})
}
