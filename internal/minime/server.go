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
	WorkerCount          int
	RunWorkers           bool
	WorkerPollInterval   time.Duration
	Generator            Generator
	GeneratorMode        string
	RepoRoot             string
	ImageGeneratorScript string
	PythonExecutable     string
	StatePipelineScript  string
	ScriptRunnerMode     string
	ScriptRunnerURL      string
	ScriptRunnerToken    string
	StoreBackend         string
	DatabaseURL          string
	StoreTable           string
	AssetBackend         string
	AssetBucket          string
	AssetRegion          string
	AssetEndpoint        string
	AssetAccessKeyID     string
	AssetSecretAccessKey string
	AssetSessionToken    string
	AssetForcePathStyle  bool
	AssetKeyPrefix       string
	AssetSignedURLTTL    time.Duration
	AssetObjectTagging   string
	AssetStorage         assetStorage
	JobTimeout           time.Duration
	SupabaseURL          string
	SupabaseAnonKey      string
	AuthHTTPClient       *http.Client
	AuthVerifier         BearerTokenVerifier
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

	assetSignedURLTTL := 15 * time.Minute
	if rawSignedURLTTL := strings.TrimSpace(os.Getenv("MINIME_ASSET_SIGNED_URL_TTL_SECONDS")); rawSignedURLTTL != "" {
		parsedSignedURLTTL, err := strconv.Atoi(rawSignedURLTTL)
		if err == nil && parsedSignedURLTTL > 0 {
			assetSignedURLTTL = time.Duration(parsedSignedURLTTL) * time.Second
		}
	}

	return Config{
		Port:                 port,
		DataRoot:             dataRoot,
		WorkerCount:          workerCount,
		RunWorkers:           runWorkers,
		WorkerPollInterval:   workerPollInterval,
		GeneratorMode:        strings.TrimSpace(os.Getenv("MINIME_GENERATOR_MODE")),
		RepoRoot:             strings.TrimSpace(os.Getenv("MINIME_REPO_ROOT")),
		ImageGeneratorScript: strings.TrimSpace(os.Getenv("MINIME_IMAGE_GENERATOR_SCRIPT")),
		PythonExecutable:     strings.TrimSpace(os.Getenv("MINIME_PYTHON_EXECUTABLE")),
		StatePipelineScript:  strings.TrimSpace(os.Getenv("MINIME_STATE_PIPELINE_SCRIPT")),
		ScriptRunnerMode:     strings.TrimSpace(os.Getenv("MINIME_SCRIPT_RUNNER_MODE")),
		ScriptRunnerURL:      strings.TrimSpace(os.Getenv("MINIME_SCRIPT_RUNNER_URL")),
		ScriptRunnerToken:    strings.TrimSpace(os.Getenv("MINIME_SCRIPT_RUNNER_TOKEN")),
		StoreBackend:         strings.TrimSpace(os.Getenv("MINIME_STORE_BACKEND")),
		DatabaseURL:          firstNonEmptyEnv("MINIME_DATABASE_URL", "DATABASE_URL"),
		StoreTable:           strings.TrimSpace(os.Getenv("MINIME_STORE_TABLE")),
		AssetBackend:         strings.TrimSpace(os.Getenv("MINIME_ASSET_BACKEND")),
		AssetBucket:          strings.TrimSpace(os.Getenv("MINIME_ASSET_BUCKET")),
		AssetRegion:          strings.TrimSpace(os.Getenv("MINIME_ASSET_REGION")),
		AssetEndpoint:        strings.TrimSpace(os.Getenv("MINIME_ASSET_ENDPOINT")),
		AssetAccessKeyID:     strings.TrimSpace(os.Getenv("MINIME_ASSET_ACCESS_KEY_ID")),
		AssetSecretAccessKey: strings.TrimSpace(os.Getenv("MINIME_ASSET_SECRET_ACCESS_KEY")),
		AssetSessionToken:    strings.TrimSpace(os.Getenv("MINIME_ASSET_SESSION_TOKEN")),
		AssetForcePathStyle:  parseBoolEnv("MINIME_ASSET_FORCE_PATH_STYLE"),
		AssetKeyPrefix:       strings.TrimSpace(os.Getenv("MINIME_ASSET_KEY_PREFIX")),
		AssetSignedURLTTL:    assetSignedURLTTL,
		AssetObjectTagging:   strings.TrimSpace(os.Getenv("MINIME_ASSET_OBJECT_TAGGING")),
		JobTimeout:           jobTimeout,
		SupabaseURL:          strings.TrimSpace(os.Getenv("SUPABASE_URL")),
		SupabaseAnonKey:      strings.TrimSpace(os.Getenv("SUPABASE_ANON_KEY")),
	}
}

type Server struct {
	config Config
	mux    *http.ServeMux

	generator    Generator
	auth         BearerTokenVerifier
	workQueue    chan string
	generationMu sync.Mutex
	mu           sync.Mutex
	sessions     map[string]*sessionRecord
	assets       map[string]*assetRecord
	jobs         map[string]*jobRecord
	queuedJobIDs map[string]struct{}
	store        storeBackend
	assetStorage assetStorage
	storeVersion int64
}

type assetRecord struct {
	ID          string `json:"id"`
	SessionID   string `json:"session_id"`
	FileName    string `json:"file_name"`
	ContentType string `json:"content_type"`
	FilePath    string `json:"file_path"`
	StorageKey  string `json:"storage_key,omitempty"`
	StorageMode string `json:"storage_mode,omitempty"`
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
	ID              string            `json:"id"`
	SessionID       string            `json:"session_id"`
	Type            string            `json:"type"`
	Status          string            `json:"status"`
	PromptSuffix    string            `json:"prompt_suffix,omitempty"`
	CandidateIndex  *int              `json:"candidate_index,omitempty"`
	CandidateCount  *int              `json:"candidate_count,omitempty"`
	RequestedStates []string          `json:"requested_states,omitempty"`
	StatePrompts    map[string]string `json:"state_prompts,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	FinishedAt      time.Time         `json:"finished_at,omitempty"`
	Summary         string            `json:"summary,omitempty"`
	Error           string            `json:"error,omitempty"`
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
	JobID          string    `json:"job_id"`
	SessionID      string    `json:"session_id"`
	Type           string    `json:"type"`
	Status         string    `json:"status"`
	CandidateIndex *int      `json:"candidate_index,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	FinishedAt     time.Time `json:"finished_at,omitempty"`
	Summary        string    `json:"summary,omitempty"`
	Error          string    `json:"error,omitempty"`
}

type selectionRequest struct {
	SelectedSourcePhotoID string `json:"selected_source_photo_id"`
	SelectedCandidateID   string `json:"selected_candidate_id"`
}

type candidatesGenerateRequest struct {
	PromptSuffix string `json:"prompt_suffix"`
}

type candidateGenerateRequest struct {
	PromptSuffix    string `json:"prompt_suffix"`
	CandidateIndex  int    `json:"candidate_index"`
	TotalCandidates int    `json:"total_candidates"`
}

type statesGenerateRequest struct {
	States       []string          `json:"states"`
	PromptSuffix string            `json:"prompt_suffix"`
	StatePrompts map[string]string `json:"state_prompts"`
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

	store, err := storeBackendForConfig(config)
	if err != nil {
		return nil, err
	}
	assetStorage, err := assetStorageForConfig(config)
	if err != nil {
		return nil, err
	}

	generator, err := generatorForConfig(config)
	if err != nil {
		return nil, err
	}
	authVerifier, err := bearerTokenVerifierForConfig(config)
	if err != nil {
		return nil, err
	}

	server := &Server{
		config:       config,
		mux:          http.NewServeMux(),
		generator:    generator,
		auth:         authVerifier,
		workQueue:    make(chan string, 64),
		sessions:     map[string]*sessionRecord{},
		assets:       map[string]*assetRecord{},
		jobs:         map[string]*jobRecord{},
		queuedJobIDs: map[string]struct{}{},
		store:        store,
		assetStorage: assetStorage,
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
		runner, err := scriptRunnerForConfig(config)
		if err != nil {
			return nil, err
		}
		return ScriptGenerator{
			RepoRoot:             config.RepoRoot,
			PythonExecutable:     config.PythonExecutable,
			ImageGeneratorScript: config.ImageGeneratorScript,
			StatePipelineScript:  config.StatePipelineScript,
			ScriptRunner:         runner,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported generator mode %q", config.GeneratorMode)
	}
}

func storeBackendForConfig(config Config) (storeBackend, error) {
	mode := strings.TrimSpace(strings.ToLower(config.StoreBackend))
	if mode == "" {
		if strings.TrimSpace(config.DatabaseURL) != "" {
			mode = "postgres"
		} else {
			mode = "file"
		}
	}

	switch mode {
	case "file":
		return newFileStoreBackend(filepath.Join(config.DataRoot, "store.json")), nil
	case "postgres":
		return newPostgresStoreBackend(context.Background(), config.DatabaseURL, config.StoreTable)
	default:
		return nil, fmt.Errorf("unsupported store backend %q", config.StoreBackend)
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
	s.mux.HandleFunc("/v1/minime/states:generate", s.handleStatelessGenerateStates)
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
		if err := s.auth.VerifyBearerToken(r.Context(), token); err != nil {
			if errors.Is(err, errUnauthorizedBearerToken) {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			writeError(w, http.StatusInternalServerError, "auth verification failed")
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
		writeStorePersistError(w, err)
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
	case "candidate":
		s.handleGenerateCandidate(w, r, session)
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
		writeStorePersistError(w, err)
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
		writeStorePersistError(w, err)
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

	var request candidatesGenerateRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid candidate generation payload")
			return
		}
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
	job.PromptSuffix = strings.TrimSpace(request.PromptSuffix)
	session.Status = "queued-candidates"
	session.CurrentStepLabel = "Queued candidate generation"
	session.Notes = "Candidate generation queued."
	session.UpdatedAt = time.Now().UTC()
	if err := s.persistStoreLocked(); err != nil {
		s.mu.Unlock()
		writeStorePersistError(w, err)
		return
	}
	snapshot := s.snapshotForSession(r, session)
	s.mu.Unlock()
	s.enqueueJob(job.ID)

	writeJSONWithHeader(w, http.StatusOK, snapshot, map[string]string{
		"X-MiniMe-Job-ID": job.ID,
	})
}

func (s *Server) handleGenerateCandidate(w http.ResponseWriter, r *http.Request, session *sessionRecord) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var request candidateGenerateRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid candidate generation payload")
			return
		}
	}
	if request.CandidateIndex <= 0 {
		writeError(w, http.StatusBadRequest, "candidate_index must be greater than zero")
		return
	}
	if request.TotalCandidates <= 0 {
		request.TotalCandidates = 4
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
	if len(session.SourcePhotos) == 0 {
		s.mu.Unlock()
		writeError(w, http.StatusBadRequest, "import source photos before generating candidates")
		return
	}
	if sessionHasActiveStateGenerationJobLocked(s, session) {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, activeGenerationError)
		return
	}
	if sessionHasQueuedOrRunningCandidateJobLocked(s, session, request.CandidateIndex) {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, fmt.Sprintf("candidate %d is already queued or running", request.CandidateIndex))
		return
	}

	job := s.createQueuedJobLocked(session, "generate-candidate", nil)
	job.PromptSuffix = strings.TrimSpace(request.PromptSuffix)
	job.CandidateIndex = &request.CandidateIndex
	job.CandidateCount = &request.TotalCandidates
	session.Status = "queued-candidates"
	session.Notes = "Candidate generation queued."
	session.UpdatedAt = time.Now().UTC()
	if err := s.persistStoreLocked(); err != nil {
		s.mu.Unlock()
		writeStorePersistError(w, err)
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
	if request.SelectedSourcePhotoID != "" && sessionHasActiveGenerationJobLocked(s, session) {
		writeError(w, http.StatusConflict, activeGenerationError)
		return
	}
	if request.SelectedCandidateID != "" && sessionHasActiveStateGenerationJobLocked(s, session) {
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
		writeStorePersistError(w, err)
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
	if sessionHasActiveStateGenerationJobLocked(s, session) {
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
	job.PromptSuffix = strings.TrimSpace(request.PromptSuffix)
	job.StatePrompts = request.StatePrompts
	session.Status = "queued-states"
	session.CurrentStepLabel = "Queued state generation"
	session.Notes = "State generation queued."
	session.UpdatedAt = time.Now().UTC()
	if err := s.persistStoreLocked(); err != nil {
		s.mu.Unlock()
		writeStorePersistError(w, err)
		return
	}
	snapshot := s.snapshotForSession(r, session)
	s.mu.Unlock()
	s.enqueueJob(job.ID)

	writeJSONWithHeader(w, http.StatusOK, snapshot, map[string]string{
		"X-MiniMe-Job-ID": job.ID,
	})
}

func (s *Server) handleStatelessGenerateStates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "expected multipart form with image and request fields")
		return
	}

	file, header, err := r.FormFile("image")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing image field")
		return
	}
	defer file.Close()

	var request statesGenerateRequest
	if requestJSON := r.FormValue("request"); requestJSON != "" {
		if err := json.Unmarshal([]byte(requestJSON), &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request JSON: "+err.Error())
			return
		}
	}

	states := request.States
	if len(states) == 0 {
		states = defaultStates
	}
	normalizedStates, err := normalizeRequestedStates(states)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.mu.Lock()

	session := &sessionRecord{
		ID:        newID("session"),
		Status:    "source-photos-imported",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	s.sessions[session.ID] = session

	asset, err := s.persistUploadedFileLocked(session.ID, "source-photos", header.Filename, header.Header.Get("Content-Type"), file)
	if err != nil {
		s.mu.Unlock()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	session.SourcePhotos = []*assetRecord{asset}
	session.SelectedSourcePhotoID = asset.ID
	session.PublishedPreview = asset

	job := s.createQueuedJobLocked(session, "generate-states", normalizedStates)
	job.PromptSuffix = strings.TrimSpace(request.PromptSuffix)
	job.StatePrompts = request.StatePrompts
	session.Status = "queued-states"
	session.CurrentStepLabel = "Queued state generation"
	session.Notes = "State generation queued."
	session.UpdatedAt = time.Now().UTC()
	if err := s.persistStoreLocked(); err != nil {
		s.mu.Unlock()
		writeStorePersistError(w, err)
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
	if strings.EqualFold(asset.StorageMode, "s3") {
		downloadURL, err := s.signedAssetDownloadURL(asset)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		http.Redirect(w, r, downloadURL, http.StatusTemporaryRedirect)
		return
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

	if s.assetStorage != nil {
		payload, err := io.ReadAll(reader)
		if err != nil {
			return nil, fmt.Errorf("read asset payload: %w", err)
		}
		asset.StorageMode = "s3"
		asset.StorageKey = assetStorageKey(sessionID, subdirectory, asset.ID, asset.FileName)
		if err := s.assetStorage.PutObject(context.Background(), asset.StorageKey, asset.ContentType, payload); err != nil {
			return nil, err
		}
		s.assets[asset.ID] = asset
		return asset, nil
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
	input, err := s.openAssetLocked(source)
	if err != nil {
		return nil, err
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
		PublishProgress: func(progress *sessionRecord) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if err := s.syncStoreLocked(); err != nil {
				return err
			}
			session := s.sessions[progress.ID]
			if session == nil {
				return errors.New("session not found")
			}
			applySessionProgressLocked(session, progress)
			now := time.Now().UTC()
			session.UpdatedAt = now
			if job := s.jobs[session.LastJobID]; job != nil && job.Status == "running" {
				job.UpdatedAt = now
				job.Summary = strings.TrimSpace(progress.CurrentStepLabel)
				if job.Summary == "" {
					job.Summary = strings.TrimSpace(progress.Notes)
				}
			}
			return s.persistStoreLocked()
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
	if existing := s.findImportedAssetLocked(sessionID, subdirectory, fileName); existing != nil {
		if err := s.replaceAssetContentsLocked(existing, input); err != nil {
			return nil, err
		}
		existing.ContentType = contentType
		return existing, nil
	}
	return s.persistUploadedFileLocked(sessionID, subdirectory, fileName, contentType, input)
}

func (s *Server) findImportedAssetLocked(sessionID, subdirectory, fileName string) *assetRecord {
	targetDirectory := filepath.Join(s.config.DataRoot, sessionID, subdirectory)
	normalizedName := sanitizeFileName(fileName)
	for _, asset := range s.assets {
		if asset == nil || asset.SessionID != sessionID {
			continue
		}
		if asset.FileName != normalizedName {
			continue
		}
		if filepath.Dir(asset.FilePath) != targetDirectory {
			continue
		}
		return asset
	}
	return nil
}

func (s *Server) replaceAssetContentsLocked(asset *assetRecord, reader io.Reader) error {
	if asset == nil {
		return errors.New("asset not found")
	}
	if strings.EqualFold(asset.StorageMode, "s3") {
		if s.assetStorage == nil {
			return errors.New("asset storage backend is unavailable")
		}
		payload, err := io.ReadAll(reader)
		if err != nil {
			return fmt.Errorf("read replacement asset payload: %w", err)
		}
		if err := s.assetStorage.PutObject(context.Background(), asset.StorageKey, asset.ContentType, payload); err != nil {
			return err
		}
		return nil
	}
	file, err := os.Create(asset.FilePath)
	if err != nil {
		return fmt.Errorf("overwrite asset file: %w", err)
	}
	defer file.Close()
	if _, err := io.Copy(file, reader); err != nil {
		return fmt.Errorf("write asset file: %w", err)
	}
	return nil
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
		JobID:          job.ID,
		SessionID:      job.SessionID,
		Type:           job.Type,
		Status:         job.Status,
		CandidateIndex: job.CandidateIndex,
		CreatedAt:      job.CreatedAt,
		UpdatedAt:      job.UpdatedAt,
		FinishedAt:     job.FinishedAt,
		Summary:        job.Summary,
		Error:          job.Error,
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
	if sessionHasOtherRunningGenerationJobLocked(s, session, job.ID) {
		s.mu.Unlock()
		go func() {
			time.Sleep(s.config.WorkerPollInterval)
			s.enqueueJob(jobID)
		}()
		return
	}

	workingSession := cloneSessionRecord(session)
	jobType := job.Type
	promptSuffix := job.PromptSuffix
	candidateIndex := 0
	if job.CandidateIndex != nil {
		candidateIndex = *job.CandidateIndex
	}
	candidateCount := 0
	if job.CandidateCount != nil {
		candidateCount = *job.CandidateCount
	}
	requestedStates := append([]string(nil), job.RequestedStates...)
	statePrompts := job.StatePrompts
	job.Status = "running"
	job.UpdatedAt = time.Now().UTC()
	switch jobType {
	case "generate-candidate":
		session.Status = "generating-candidates"
		session.Notes = "Generating Mini Me candidate."
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
		if errors.Is(err, errStoreVersionConflict) {
			s.failJobLocked(job, fmt.Errorf("store conflict while persisting queued job start: %w", err))
		} else {
			s.failJobLocked(job, err)
		}
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
	err = s.runGenerationExclusive(func() error {
		switch jobType {
		case "generate-candidate":
			return s.generator.GenerateCandidate(jobContext, s.generationEnvironment(), workingSession, candidateIndex, candidateCount, promptSuffix)
		case "generate-candidates":
			return s.generator.GenerateCandidates(jobContext, s.generationEnvironment(), workingSession, promptSuffix)
		case "generate-states":
			return s.generator.GenerateStates(jobContext, s.generationEnvironment(), workingSession, requestedStates, promptSuffix, statePrompts)
		default:
			return fmt.Errorf("unsupported queued job type %q", jobType)
		}
	})

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
	case "generate-candidate":
		s.completeJobLocked(job, fmt.Sprintf("Candidate %d generated.", candidateIndex))
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

func (s *Server) runGenerationExclusive(run func() error) error {
	s.generationMu.Lock()
	defer s.generationMu.Unlock()
	return run()
}

func (s *Server) remoteAssetRecord(r *http.Request, asset *assetRecord) remoteAssetRecord {
	if asset == nil {
		return remoteAssetRecord{}
	}
	downloadURL := absoluteURL(r, "/v1/minime/assets/"+asset.ID)
	if strings.EqualFold(asset.StorageMode, "s3") {
		signedURL, err := s.signedAssetDownloadURL(asset)
		if err == nil && signedURL != "" {
			downloadURL = signedURL
		}
	}
	return remoteAssetRecord{
		ID:          asset.ID,
		Filename:    asset.FileName,
		DownloadURL: downloadURL,
	}
}

func (s *Server) openAssetLocked(asset *assetRecord) (io.ReadCloser, error) {
	if asset == nil {
		return nil, errors.New("asset not found")
	}
	if strings.EqualFold(asset.StorageMode, "s3") {
		if s.assetStorage == nil {
			return nil, errors.New("asset storage backend is unavailable")
		}
		body, err := s.assetStorage.GetObject(context.Background(), asset.StorageKey)
		if err != nil {
			return nil, err
		}
		return body, nil
	}
	input, err := os.Open(asset.FilePath)
	if err != nil {
		return nil, fmt.Errorf("open source asset: %w", err)
	}
	return input, nil
}

func (s *Server) signedAssetDownloadURL(asset *assetRecord) (string, error) {
	if s.assetStorage == nil {
		return "", errors.New("asset storage backend is unavailable")
	}
	return s.assetStorage.SignedGetURL(context.Background(), asset.StorageKey, asset.FileName, s.config.AssetSignedURLTTL)
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

	version, err := s.store.Save(context.Background(), data, s.storeVersion)
	if err != nil {
		return err
	}
	s.storeVersion = version
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
	data, version, err := s.store.Load(context.Background())
	if errors.Is(err, errStoreNotFound) {
		s.sessions = map[string]*sessionRecord{}
		s.assets = map[string]*assetRecord{}
		s.jobs = map[string]*jobRecord{}
		s.storeVersion = 0
		return nil
	}
	if err != nil {
		return err
	}

	var store persistedStore
	if err := json.Unmarshal(data, &store); err != nil {
		return fmt.Errorf("decode store file: %w", err)
	}

	s.sessions = map[string]*sessionRecord{}
	s.assets = map[string]*assetRecord{}
	s.jobs = map[string]*jobRecord{}
	s.storeVersion = version
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

func writeStorePersistError(w http.ResponseWriter, err error) {
	if errors.Is(err, errStoreVersionConflict) {
		writeError(w, http.StatusConflict, "store changed concurrently; retry request")
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		value := strings.TrimSpace(os.Getenv(key))
		if value != "" {
			return value
		}
	}
	return ""
}

func parseBoolEnv(key string) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return false
	}
	return value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "yes")
}

func assetStorageKey(sessionID, subdirectory, assetID, fileName string) string {
	return strings.Trim(strings.Join([]string{
		sanitizeFileName(sessionID),
		sanitizeFileName(subdirectory),
		sanitizeFileName(assetID + "-" + fileName),
	}, "/"), "/")
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

func applySessionProgressLocked(session *sessionRecord, progress *sessionRecord) {
	if session == nil || progress == nil {
		return
	}

	session.Status = progress.Status
	session.CurrentStepLabel = progress.CurrentStepLabel
	session.Notes = progress.Notes
	session.SelectedSourcePhotoID = progress.SelectedSourcePhotoID
	session.SelectedCandidateID = progress.SelectedCandidateID
	session.PublishedPreview = progress.PublishedPreview
	session.CurrentIndex = nil
	if progress.CurrentIndex != nil {
		currentIndex := *progress.CurrentIndex
		session.CurrentIndex = &currentIndex
	}
	session.TotalCount = nil
	if progress.TotalCount != nil {
		totalCount := *progress.TotalCount
		session.TotalCount = &totalCount
	}
	session.Candidates = append([]*assetRecord(nil), progress.Candidates...)
	session.StateAssets = map[string]*stateAssetRecord{}
	for stateName, asset := range progress.StateAssets {
		if asset == nil {
			session.StateAssets[stateName] = nil
			continue
		}
		copiedAsset := *asset
		session.StateAssets[stateName] = &copiedAsset
	}
}

func sessionHasActiveGenerationJobLocked(server *Server, session *sessionRecord) bool {
	if server == nil || session == nil {
		return false
	}
	for _, job := range server.jobs {
		if job.SessionID != session.ID {
			continue
		}
		if job.Type != "generate-candidates" && job.Type != "generate-candidate" && job.Type != "generate-states" {
			continue
		}
		if job.Status == "queued" || job.Status == "running" {
			return true
		}
	}
	return false
}

func sessionHasActiveStateGenerationJobLocked(server *Server, session *sessionRecord) bool {
	if server == nil || session == nil {
		return false
	}
	for _, job := range server.jobs {
		if job.SessionID != session.ID || job.Type != "generate-states" {
			continue
		}
		if job.Status == "queued" || job.Status == "running" {
			return true
		}
	}
	return false
}

func sessionHasQueuedOrRunningCandidateJobLocked(server *Server, session *sessionRecord, candidateIndex int) bool {
	if server == nil || session == nil {
		return false
	}
	for _, job := range server.jobs {
		if job.SessionID != session.ID || job.Type != "generate-candidate" {
			continue
		}
		if job.CandidateIndex == nil || *job.CandidateIndex != candidateIndex {
			continue
		}
		if job.Status == "queued" || job.Status == "running" {
			return true
		}
	}
	return false
}

func sessionHasOtherRunningGenerationJobLocked(server *Server, session *sessionRecord, excludingJobID string) bool {
	if server == nil || session == nil {
		return false
	}
	for _, job := range server.jobs {
		if job.ID == excludingJobID || job.SessionID != session.ID {
			continue
		}
		if job.Type != "generate-candidate" && job.Type != "generate-candidates" && job.Type != "generate-states" {
			continue
		}
		if job.Status == "running" {
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
