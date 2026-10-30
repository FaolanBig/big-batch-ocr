package main

import (
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxRequestBytes int64 = 1 << 30
const maxMultipartTextFieldBytes int64 = 1 << 20

type archiveUpload struct {
	path string
}

func (a *archiveUpload) Open() (io.ReadCloser, error) {
	return os.Open(a.path)
}

func (a *archiveUpload) Remove() error {
	return os.Remove(a.path)
}

type API struct {
	config      Config
	jobs        *JobService
	store       *JobStore
	logger      *slog.Logger
	mux         *http.ServeMux
	maintenance sync.RWMutex
}

type submitRequest struct {
	InputPath                      string          `json:"inputPath"`
	OutputPath                     string          `json:"outputPath"`
	Mode                           string          `json:"mode"`
	CreateDestination              json.RawMessage `json:"createDestination"`
	LanguageValue                  json.RawMessage `json:"language"`
	OptimizeValue                  json.RawMessage `json:"optimize"`
	WebhookURL                     string          `json:"webhookUrl"`
	WebhookSecret                  string          `json:"webhookSecret"`
	RootPath                       string          `json:"rootPath"`
	WebhookInsecureSkipVerifyValue json.RawMessage `json:"webhookInsecureSkipVerify"`
	WebhookInsecureSkipVerify      bool            `json:"-"`
	Language                       *string         `json:"-"`
	Optimize                       *int            `json:"-"`
	CreateDestinationValue         bool            `json:"-"`
}

type acknowledgement struct {
	JobID     string `json:"jobId"`
	Status    string `json:"status"`
	StatusURL string `json:"statusUrl"`
}

type cleanupRequest struct {
	Scope string `json:"scope"`
}

type cleanupResult struct {
	Scope                     string `json:"scope"`
	JobsDeleted               int    `json:"jobsDeleted"`
	ActiveJobsSkipped         int    `json:"activeJobsSkipped"`
	JobsSkippedForActiveData  int    `json:"jobsSkippedForActiveData"`
	JobsSkippedForServerData  int    `json:"jobsSkippedForServerData"`
	JobsSkippedForUnsafePaths int    `json:"jobsSkippedForUnsafePaths"`
}

type problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Code      string `json:"code"`
	Detail    string `json:"detail,omitempty"`
	Instance  string `json:"instance,omitempty"`
	JobID     string `json:"jobId,omitempty"`
	JobStatus string `json:"jobStatus,omitempty"`
	StatusURL string `json:"statusUrl,omitempty"`
}

func NewAPI(config Config, jobs *JobService, store *JobStore, logger *slog.Logger) *API {
	api := &API{config: config, jobs: jobs, store: store, logger: logger, mux: http.NewServeMux()}
	api.mux.HandleFunc("GET /health", api.health)
	api.mux.Handle("POST /api/v1/jobs", api.authenticate(http.HandlerFunc(api.submit)))
	api.mux.Handle("POST /api/v1/jobs/cleanup", api.authenticate(http.HandlerFunc(api.cleanupJobs)))
	api.mux.Handle("GET /api/v1/jobs/{jobId}", api.authenticate(http.HandlerFunc(api.getJob)))
	api.mux.Handle("DELETE /api/v1/jobs/{jobId}", api.authenticate(http.HandlerFunc(api.deleteJob)))
	api.mux.Handle("POST /api/v1/jobs/{jobId}/cancel", api.authenticate(http.HandlerFunc(api.cancelJob)))
	api.mux.Handle("POST /api/v1/jobs/{jobId}/resume", api.authenticate(http.HandlerFunc(api.resumeJob)))
	api.mux.Handle("GET /api/v1/jobs/{jobId}/result", api.authenticate(http.HandlerFunc(api.getJobResult)))
	return api
}

func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	recorder := &responseRecorder{ResponseWriter: w}
	started := time.Now()
	defer func() {
		if recovered := recover(); recovered != nil {
			if !recorder.wroteHeader {
				recorder.status = http.StatusInternalServerError
			}
			a.logRequest(r, recorder, started)
			panic(recovered)
		}
		a.logRequest(r, recorder, started)
	}()
	a.mux.ServeHTTP(recorder, r)
}

func (a *API) logRequest(r *http.Request, response *responseRecorder, started time.Time) {
	status := response.status
	if status == 0 {
		status = http.StatusOK
	}
	level := slog.LevelInfo
	if status >= http.StatusInternalServerError {
		level = slog.LevelError
	} else if status >= http.StatusBadRequest {
		level = slog.LevelWarn
	}
	a.logger.Log(r.Context(), level, "HTTP request completed",
		"method", r.Method,
		"path", r.URL.Path,
		"status", status,
		"bytes_written", response.bytesWritten,
		"duration_ms", time.Since(started).Milliseconds(),
	)
}

type responseRecorder struct {
	http.ResponseWriter
	status       int
	bytesWritten int64
	wroteHeader  bool
}

func (w *responseRecorder) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseRecorder) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(body)
	w.bytesWritten += int64(n)
	return n, err
}

func (w *responseRecorder) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (a *API) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.config.AllowAnonymous {
			next.ServeHTTP(w, r)
			return
		}
		scheme, key, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || key == "" ||
			subtle.ConstantTimeCompare([]byte(key), []byte(a.config.APIKey)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeProblem(w, http.StatusUnauthorized, "Unauthorized", "UNAUTHENTICATED",
				"A valid bearer API key is required.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) submit(w http.ResponseWriter, r *http.Request) {
	a.maintenance.RLock()
	defer a.maintenance.RUnlock()
	request, archive, err := parseSubmitRequest(w, r)
	if archive != nil {
		defer func() {
			if err := archive.Remove(); err != nil && !errors.Is(err, os.ErrNotExist) {
				a.logger.Warn("could not remove temporary archive upload", "error", err)
			}
		}()
	}
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeProblem(w, http.StatusRequestEntityTooLarge, "Request too large", "REQUEST_TOO_LARGE",
				"The request body exceeds its size limit.")
			return
		}
		a.logger.Warn("invalid job submission request", "error", err)
		writeProblem(w, http.StatusBadRequest, "Invalid request", "INVALID_REQUEST",
			"The request body is not valid for this operation.")
		return
	}
	options := OCROptions{Language: a.config.OCRLanguage, Optimize: a.config.OCROptimize}
	if request.Language != nil {
		options.Language, err = validateOCRLanguage(*request.Language)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "Invalid OCR options", "INVALID_OCR_OPTIONS",
				"language must contain valid OCR language codes separated by '+'.")
			return
		}
	}
	if request.Optimize != nil {
		options.Optimize, err = validateOCROptimize(*request.Optimize)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "Invalid OCR options", "INVALID_OCR_OPTIONS",
				"optimize must be an integer from 0 to 3.")
			return
		}
	}
	if err := validateWebhookOptions(request.WebhookURL, request.WebhookSecret, request.RootPath,
		request.WebhookInsecureSkipVerify); err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid webhook options", "INVALID_WEBHOOK_OPTIONS", err.Error())
		return
	}
	options.WebhookURL = request.WebhookURL
	options.WebhookSecret = request.WebhookSecret
	options.RootPath = request.RootPath
	options.WebhookInsecureSkipVerify = request.WebhookInsecureSkipVerify
	if archive == nil && !a.config.AllowDirectoryAccess {
		writeProblem(w, http.StatusForbidden, "Directory submissions disabled", "DIRECTORY_ACCESS_DISABLED",
			"Directory-based submissions are disabled; upload an inputArchive instead.")
		return
	}
	var uploadDirectory string
	var uploadedJobID string
	if archive != nil {
		uploadedJobID, err = newJobID()
		if err != nil {
			a.logger.Error("could not allocate OCR job ID", "error", err)
			writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
				"The input archive could not be accepted.")
			return
		}
		uploadDirectory, err = createJobWorkspace(a.config.DataRoot, uploadedJobID)
		if err != nil {
			a.logger.Error("could not create archive job workspace", "job_id", uploadedJobID, "error", err)
			writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
				"The input archive could not be accepted.")
			return
		}
		defer func() {
			if uploadDirectory != "" {
				if err := os.RemoveAll(uploadDirectory); err != nil {
					a.logger.Warn("could not remove rejected archive upload", "error", err)
				}
			}
		}()
		input, openErr := archive.Open()
		if openErr != nil {
			writeProblem(w, http.StatusBadRequest, "Invalid archive", "INVALID_ARCHIVE",
				"The uploaded file must be a valid tar.gz archive containing regular files and directories.")
			return
		}
		inputDirectory := filepath.Join(uploadDirectory, "input")
		extractErr := extractTarGzip(input, inputDirectory)
		closeErr := input.Close()
		if err := errors.Join(extractErr, closeErr); err != nil {
			writeProblem(w, http.StatusBadRequest, "Invalid archive", "INVALID_ARCHIVE",
				"The uploaded file must be a valid tar.gz archive containing regular files and directories.")
			return
		}
		relative, err := filepath.Rel(a.config.DataRoot, inputDirectory)
		if err != nil {
			a.logger.Error("could not resolve archive upload path", "error", err)
			writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
				"The input archive could not be accepted.")
			return
		}
		request.InputPath = filepath.ToSlash(relative)
		request.OutputPath = filepath.ToSlash(filepath.Join(".api-host", "jobs", uploadedJobID, "output"))
	}
	if strings.TrimSpace(request.InputPath) == "" || strings.TrimSpace(request.OutputPath) == "" ||
		(request.Mode != "synchronous" && request.Mode != "asynchronous") {
		writeProblem(w, http.StatusBadRequest, "Invalid request", "INVALID_REQUEST",
			"The request body is not valid for this operation.")
		return
	}
	paths, err := validatePaths(a.config.DataRoot, a.config.DockerDataRoot,
		request.InputPath, request.OutputPath, request.CreateDestinationValue, uploadedJobID != "")
	if errors.Is(err, ErrPathConflict) {
		writeProblem(w, http.StatusConflict, "Incompatible directories", "INCOMPATIBLE_PATHS",
			"Input and destination directories must be distinct and must not contain one another.")
		return
	}
	if err != nil {
		writeProblem(w, http.StatusUnprocessableEntity, "Invalid path", "INVALID_PATH",
			"The input and destination must satisfy the configured data-root directory rules.")
		return
	}
	var job Job
	if uploadedJobID != "" {
		job, err = a.jobs.SubmitWithID(uploadedJobID, paths.InputPath, paths.OutputPath, paths.InputHost, paths.OutputHost, options)
	} else {
		job, err = a.jobs.Submit(paths.InputPath, paths.OutputPath, paths.InputHost, paths.OutputHost, options)
	}
	if err != nil {
		a.logger.Error("could not persist OCR job", "error", err)
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
			"The job could not be accepted.")
		return
	}
	uploadDirectory = ""
	a.logger.Info("OCR job accepted", "job_id", job.ID, "mode", request.Mode)
	if request.Mode == "asynchronous" {
		w.Header().Set("Location", job.StatusURL)
		writeJSON(w, http.StatusAccepted, acknowledgement{JobID: job.ID, Status: job.Status, StatusURL: job.StatusURL})
		return
	}
	a.waitSynchronously(w, r, job.ID)
}

func parseSubmitRequest(w http.ResponseWriter, r *http.Request) (submitRequest, *archiveUpload, error) {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil && r.Header.Get("Content-Type") != "" {
		return submitRequest{}, nil, err
	}
	if contentType == "multipart/form-data" {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
		reader, err := r.MultipartReader()
		if err != nil {
			return submitRequest{}, nil, err
		}
		return parseMultipartSubmitRequest(reader)
	}

	if contentType != "" && contentType != "application/json" {
		return submitRequest{}, nil, errors.New("unsupported content type")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request submitRequest
	if err := decoder.Decode(&request); err != nil {
		return submitRequest{}, nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return submitRequest{}, nil, errors.New("request must contain exactly one JSON object")
	}
	request.CreateDestinationValue = false
	if len(request.CreateDestination) != 0 {
		trimmed := strings.TrimSpace(string(request.CreateDestination))
		if trimmed == "null" || json.Unmarshal(request.CreateDestination, &request.CreateDestinationValue) != nil {
			return submitRequest{}, nil, errors.New("createDestination must be a boolean")
		}
	}
	if len(request.LanguageValue) != 0 {
		trimmed := strings.TrimSpace(string(request.LanguageValue))
		var language string
		if trimmed == "null" || json.Unmarshal(request.LanguageValue, &language) != nil {
			return submitRequest{}, nil, errors.New("language must be a string")
		}
		request.Language = &language
	}
	if len(request.OptimizeValue) != 0 {
		trimmed := strings.TrimSpace(string(request.OptimizeValue))
		var optimize int
		if trimmed == "null" || json.Unmarshal(request.OptimizeValue, &optimize) != nil {
			return submitRequest{}, nil, errors.New("optimize must be an integer")
		}
		request.Optimize = &optimize
	}
	if len(request.WebhookInsecureSkipVerifyValue) != 0 {
		trimmed := strings.TrimSpace(string(request.WebhookInsecureSkipVerifyValue))
		if trimmed == "null" ||
			json.Unmarshal(request.WebhookInsecureSkipVerifyValue, &request.WebhookInsecureSkipVerify) != nil {
			return submitRequest{}, nil, errors.New("webhookInsecureSkipVerify must be a boolean")
		}
	}
	return request, nil, nil
}

func parseMultipartSubmitRequest(reader *multipart.Reader) (request submitRequest, archive *archiveUpload, err error) {
	defer func() {
		if err != nil && archive != nil {
			err = errors.Join(err, archive.Remove())
			archive = nil
		}
	}()

	fields := make(map[string]string)
	seen := make(map[string]bool)
	archiveField := ""
	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return submitRequest{}, archive, nextErr
		}
		name := part.FormName()
		if name == "" || seen[name] {
			return submitRequest{}, archive, errors.New("multipart field is missing a name or repeated")
		}
		seen[name] = true

		switch name {
		case "inputArchive", "inputArchiveContents", "inputArchiveContentsBase64":
			if archiveField != "" {
				return submitRequest{}, archive, errors.New("only one archive input is allowed")
			}
			if name == "inputArchiveContentsBase64" && part.FileName() != "" {
				return submitRequest{}, archive, errors.New("base64 archive contents must be a text field")
			}
			if filename := part.FileName(); filename != "" &&
				!strings.HasSuffix(strings.ToLower(filename), ".tar.gz") {
				return submitRequest{}, archive, errors.New("input archive must have a .tar.gz filename")
			}
			archiveField = name
			file, createErr := os.CreateTemp("", "big-batch-ocr-archive-*.tar.gz")
			if createErr != nil {
				return submitRequest{}, archive, createErr
			}
			archive = &archiveUpload{path: file.Name()}
			source := io.Reader(part)
			if name == "inputArchiveContentsBase64" {
				source = base64.NewDecoder(base64.StdEncoding.Strict(), part)
			}
			_, copyErr := io.Copy(file, source)
			closeErr := file.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				return submitRequest{}, archive, err
			}
		case "inputPath", "outputPath", "mode", "createDestination", "language", "optimize",
			"webhookUrl", "webhookSecret", "rootPath", "webhookInsecureSkipVerify":
			if part.FileName() != "" {
				return submitRequest{}, archive, errors.New("multipart text field must not be a file")
			}
			value, readErr := io.ReadAll(io.LimitReader(part, maxMultipartTextFieldBytes+1))
			if readErr != nil {
				return submitRequest{}, archive, readErr
			}
			if int64(len(value)) > maxMultipartTextFieldBytes {
				return submitRequest{}, archive, errors.New("multipart text field is too large")
			}
			fields[name] = string(value)
		default:
			return submitRequest{}, archive, errors.New("unknown multipart field")
		}
		if closeErr := part.Close(); closeErr != nil {
			return submitRequest{}, archive, closeErr
		}
	}

	inputPath := fields["inputPath"]
	outputPath := fields["outputPath"]
	mode := fields["mode"]
	if strings.TrimSpace(mode) == "" {
		return submitRequest{}, archive, errors.New("multipart mode is required")
	}
	create := false
	if createDestination := fields["createDestination"]; createDestination != "" {
		switch strings.ToLower(createDestination) {
		case "true":
			create = true
		case "false":
		default:
			return submitRequest{}, archive, errors.New("createDestination must be true or false")
		}
	}
	if seen["language"] {
		value := fields["language"]
		request.Language = &value
	}
	if seen["optimize"] {
		parsed, parseErr := parseOCROptimize(fields["optimize"])
		if parseErr != nil {
			return submitRequest{}, archive, errors.New("optimize must be an integer")
		}
		request.Optimize = &parsed
	}
	if seen["webhookInsecureSkipVerify"] {
		switch strings.ToLower(fields["webhookInsecureSkipVerify"]) {
		case "true":
			request.WebhookInsecureSkipVerify = true
		case "false":
		default:
			return submitRequest{}, archive, errors.New("webhookInsecureSkipVerify must be true or false")
		}
	}
	if archive != nil && (inputPath != "" || outputPath != "") {
		return submitRequest{}, archive, errors.New("archive uploads cannot include inputPath or outputPath")
	}
	if archive == nil && inputPath == "" {
		return submitRequest{}, nil, errors.New("inputPath is required without inputArchive")
	}
	request = submitRequest{
		InputPath: inputPath, OutputPath: outputPath, Mode: mode,
		CreateDestinationValue: create, Language: request.Language, Optimize: request.Optimize,
		WebhookURL: fields["webhookUrl"], WebhookSecret: fields["webhookSecret"], RootPath: fields["rootPath"],
		WebhookInsecureSkipVerify: request.WebhookInsecureSkipVerify,
	}
	return request, archive, nil
}

func validateWebhookOptions(webhookURL, webhookSecret, rootPath string, insecureSkipVerify bool) error {
	if webhookURL == "" {
		if webhookSecret != "" || rootPath != "" || insecureSkipVerify {
			return errors.New("webhookSecret, rootPath, and webhookInsecureSkipVerify require webhookUrl")
		}
		return nil
	}
	if len(webhookURL) > 2048 {
		return errors.New("webhookUrl must not exceed 2048 characters")
	}
	parsed, err := url.Parse(webhookURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("webhookUrl must be an absolute HTTP or HTTPS URL without user information or a fragment")
	}
	if len(webhookSecret) > 4096 || hasHeaderControlCharacters(webhookSecret) {
		return errors.New("webhookSecret must be at most 4096 characters and contain no control characters")
	}
	if len(rootPath) > 4096 || hasHeaderControlCharacters(rootPath) {
		return errors.New("rootPath must be at most 4096 characters and contain no control characters")
	}
	return nil
}

func hasHeaderControlCharacters(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

func createJobWorkspace(dataRoot, jobID string) (string, error) {
	root, err := filepath.EvalSymlinks(dataRoot)
	if err != nil {
		return "", fmt.Errorf("resolve data root: %w", err)
	}
	privateRoot := filepath.Join(root, ".api-host")
	jobsRoot := filepath.Join(privateRoot, "jobs")
	for _, directory := range []string{privateRoot, jobsRoot} {
		if err := os.Mkdir(directory, 0o750); err != nil && !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("create private job directory: %w", err)
		}
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("private job path is not a real directory")
		}
	}
	directory := filepath.Join(jobsRoot, jobID)
	if err := os.Mkdir(directory, 0o750); err != nil {
		return "", fmt.Errorf("create job workspace: %w", err)
	}
	for _, subdirectory := range []string{"input", "output"} {
		if err := os.Mkdir(filepath.Join(directory, subdirectory), 0o750); err != nil {
			cleanupErr := os.RemoveAll(directory)
			return "", errors.Join(fmt.Errorf("create job %s directory: %w", subdirectory, err), cleanupErr)
		}
	}
	return directory, nil
}

func isUploadedJob(job Job) bool {
	jobRoot := filepath.Join(".api-host", "jobs", job.ID)
	return filepath.Clean(filepath.FromSlash(job.InputPath)) == filepath.Join(jobRoot, "input") &&
		filepath.Clean(filepath.FromSlash(job.OutputPath)) == filepath.Join(jobRoot, "output")
}

func removeUploadedJobWorkspace(dataRoot string, job Job) error {
	if !isUploadedJob(job) {
		return nil
	}
	root, err := filepath.EvalSymlinks(dataRoot)
	if err != nil {
		return fmt.Errorf("resolve data root: %w", err)
	}
	jobsRoot := filepath.Join(root, ".api-host", "jobs")
	jobDirectory := filepath.Join(jobsRoot, job.ID)
	if !within(jobsRoot, jobDirectory) || filepath.Base(jobDirectory) != job.ID {
		return errors.New("job workspace path is invalid")
	}
	for _, directory := range []string{filepath.Dir(jobsRoot), jobsRoot} {
		info, err := os.Lstat(directory)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect job workspace parent: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("job workspace parent is not a real directory")
		}
	}
	return os.RemoveAll(jobDirectory)
}

func resolveJobDirectory(dataRoot, requestPath string) (string, bool, error) {
	root, err := filepath.EvalSymlinks(dataRoot)
	if err != nil {
		return "", false, fmt.Errorf("resolve data root: %w", err)
	}
	relative, err := safeRelativePath(root, requestPath, false)
	if err != nil {
		return "", false, err
	}
	privatePath := filepath.Join(".api-host")
	if relative == privatePath || strings.HasPrefix(relative, privatePath+string(filepath.Separator)) {
		return "", false, errors.New("job directory points into API host private data")
	}
	candidate := filepath.Join(root, filepath.FromSlash(relative))
	resolved, err := filepath.EvalSymlinks(candidate)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("resolve job directory: %w", err)
	}
	if !within(root, resolved) || resolved == root ||
		resolved == filepath.Join(root, privatePath) ||
		within(filepath.Join(root, privatePath), resolved) {
		return "", false, errors.New("job directory is outside the removable data area")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", false, fmt.Errorf("inspect job directory: %w", err)
	}
	if !info.IsDir() {
		return "", false, errors.New("job path is no longer a directory")
	}
	return filepath.Clean(resolved), true, nil
}

func removeJobDirectory(dataRoot, requestPath string) error {
	directory, exists, err := resolveJobDirectory(dataRoot, requestPath)
	if err != nil || !exists {
		return err
	}
	if err := os.RemoveAll(directory); err != nil {
		return fmt.Errorf("remove job directory: %w", err)
	}
	return nil
}

func terminalJob(status string) bool {
	return status == "succeeded" || status == "failed" || status == "cancelled"
}

func pathsOverlap(left, right string) bool {
	return left == right || within(left, right) || within(right, left)
}

func pathContains(parent, target string) bool {
	relative, err := filepath.Rel(parent, target)
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)))
}

func (a *API) cleanupJobs(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request cleanupRequest
	if err := decoder.Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid request", "INVALID_REQUEST",
			"The cleanup scope must be `uploads` or `all`.")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeProblem(w, http.StatusBadRequest, "Invalid request", "INVALID_REQUEST",
			"The request must contain exactly one JSON object.")
		return
	}
	if request.Scope != "uploads" && request.Scope != "all" {
		writeProblem(w, http.StatusBadRequest, "Invalid cleanup scope", "INVALID_CLEANUP_SCOPE",
			"The cleanup scope must be `uploads` or `all`.")
		return
	}

	a.maintenance.Lock()
	defer a.maintenance.Unlock()
	unlock := a.jobs.LockMaintenance()
	defer unlock()
	allJobs, err := a.store.List()
	if err != nil {
		a.logger.Error("could not list OCR jobs for cleanup", "error", err)
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
			"Job data could not be cleaned up.")
		return
	}
	result := cleanupResult{Scope: request.Scope}
	var activePaths []string
	activePathsUnsafe := false
	for _, job := range allJobs {
		if terminalJob(job.Status) {
			continue
		}
		result.ActiveJobsSkipped++
		if request.Scope != "all" || isUploadedJob(job) {
			continue
		}
		for _, relative := range []string{job.InputPath, job.OutputPath} {
			directory, exists, err := resolveJobDirectory(a.config.DataRoot, relative)
			if err != nil {
				a.logger.Warn("could not resolve active OCR job path during cleanup; protecting shared directories", "job_id", job.ID, "error", err)
				activePathsUnsafe = true
				continue
			}
			if exists {
				activePaths = append(activePaths, directory)
			}
		}
	}

	for _, job := range allJobs {
		if !terminalJob(job.Status) || (request.Scope == "uploads" && !isUploadedJob(job)) {
			continue
		}
		if request.Scope == "all" && !isUploadedJob(job) {
			protected := activePathsUnsafe
			serverData := false
			unsafePath := false
			for _, relative := range []string{job.InputPath, job.OutputPath} {
				directory, exists, err := resolveJobDirectory(a.config.DataRoot, relative)
				if err != nil {
					a.logger.Warn("could not resolve terminal OCR job path during cleanup; preserving job data", "job_id", job.ID, "error", err)
					unsafePath = true
					continue
				}
				if !exists {
					continue
				}
				if pathContains(directory, filepath.Clean(a.config.DatabasePath)) {
					serverData = true
					break
				}
				for _, activePath := range activePaths {
					if pathsOverlap(directory, activePath) {
						protected = true
						break
					}
				}
				if protected {
					break
				}
			}
			if unsafePath {
				result.JobsSkippedForUnsafePaths++
				continue
			}
			if protected {
				result.JobsSkippedForActiveData++
				continue
			}
			if serverData {
				result.JobsSkippedForServerData++
				continue
			}
			for _, relative := range []string{job.InputPath, job.OutputPath} {
				if err := removeJobDirectory(a.config.DataRoot, relative); err != nil {
					a.logger.Error("could not remove terminal OCR job directory", "job_id", job.ID, "error", err)
					writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
						"Job data could not be cleaned up.")
					return
				}
			}
		} else if err := removeUploadedJobWorkspace(a.config.DataRoot, job); err != nil {
			a.logger.Error("could not remove uploaded OCR job workspace during cleanup", "job_id", job.ID, "error", err)
			writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
				"Job data could not be cleaned up.")
			return
		}
		if err := a.store.DeleteTerminal(job.ID); err != nil {
			a.logger.Error("could not delete terminal OCR job during cleanup", "job_id", job.ID, "error", err)
			writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
				"Job data could not be cleaned up.")
			return
		}
		result.JobsDeleted++
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *API) waitSynchronously(w http.ResponseWriter, r *http.Request, id string) {
	deadline := time.NewTimer(a.config.SyncWaitLimit)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		job, err := a.jobs.Get(id)
		if err != nil {
			a.logger.Error("could not read synchronous OCR job", "job_id", id, "error", err)
			writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
				"The job status could not be read.")
			return
		}
		switch job.Status {
		case "succeeded":
			a.writeJobResultArchive(w, job)
			return
		case "failed":
			writeJSONContentType(w, http.StatusInternalServerError, problem{
				Type: "about:blank", Title: "OCR execution failed", Status: http.StatusInternalServerError,
				Code: "OCR_EXECUTION_FAILED", Detail: "The OCR container exited unsuccessfully.",
				JobID: job.ID, JobStatus: job.Status, StatusURL: job.StatusURL,
			}, "application/problem+json")
			return
		case "cancelled":
			writeJSONContentType(w, http.StatusConflict, problem{
				Type: "about:blank", Title: "OCR job cancelled", Status: http.StatusConflict,
				Code: "JOB_CANCELLED", Detail: "The OCR job was cancelled.",
				JobID: job.ID, JobStatus: job.Status, StatusURL: job.StatusURL,
			}, "application/problem+json")
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			job, err = a.jobs.Get(id)
			if err != nil {
				a.logger.Error("could not read timed-out OCR job", "job_id", id, "error", err)
				writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
					"The job status could not be read.")
				return
			}
			if job.Status == "succeeded" || job.Status == "failed" || job.Status == "cancelled" {
				continue
			}
			writeJSONContentType(w, http.StatusGatewayTimeout, problem{
				Type: "about:blank", Title: "Synchronous wait limit exceeded",
				Status: http.StatusGatewayTimeout, Code: "SYNC_WAIT_TIMEOUT",
				Detail: "The job is still running and was not cancelled.",
				JobID:  job.ID, JobStatus: job.Status, StatusURL: job.StatusURL,
			}, "application/problem+json")
			return
		case <-ticker.C:
		}
	}
}

func (a *API) getJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("jobId")
	job, err := a.jobs.Get(id)
	if errors.Is(err, sql.ErrNoRows) {
		writeProblem(w, http.StatusNotFound, "Job not found", "JOB_NOT_FOUND",
			"No job was found for the supplied identifier.")
		return
	}
	if err != nil {
		a.logger.Error("could not read OCR job", "job_id", id, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
			"The job status could not be read.")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (a *API) cancelJob(w http.ResponseWriter, r *http.Request) {
	a.maintenance.RLock()
	defer a.maintenance.RUnlock()
	id := r.PathValue("jobId")
	job, err := a.jobs.Cancel(id)
	if errors.Is(err, sql.ErrNoRows) {
		writeProblem(w, http.StatusNotFound, "Job not found", "JOB_NOT_FOUND",
			"No job was found for the supplied identifier.")
		return
	}
	if err != nil {
		a.logger.Error("could not cancel OCR job", "job_id", id, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
			"The job could not be cancelled.")
		return
	}
	switch job.Status {
	case "cancelled":
		writeJSON(w, http.StatusOK, job)
	case "cancelling":
		writeJSON(w, http.StatusAccepted, job)
	default:
		writeProblem(w, http.StatusConflict, "Job is already terminal", "JOB_NOT_CANCELLABLE",
			"Only queued or running jobs can be cancelled.")
	}
}

func (a *API) resumeJob(w http.ResponseWriter, r *http.Request) {
	a.maintenance.RLock()
	defer a.maintenance.RUnlock()
	id := r.PathValue("jobId")
	job, err := a.jobs.Resume(id)
	if errors.Is(err, sql.ErrNoRows) {
		writeProblem(w, http.StatusNotFound, "Job not found", "JOB_NOT_FOUND",
			"No job was found for the supplied identifier.")
		return
	}
	if errors.Is(err, ErrJobNotResumable) {
		writeProblem(w, http.StatusConflict, "Job cannot be resumed", "JOB_NOT_RESUMABLE",
			"Only failed or cancelled jobs can be resumed.")
		return
	}
	if err != nil {
		a.logger.Error("could not resume OCR job", "job_id", id, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
			"The job could not be resumed.")
		return
	}
	w.Header().Set("Location", job.StatusURL)
	writeJSON(w, http.StatusAccepted, acknowledgement{JobID: job.ID, Status: "queued", StatusURL: job.StatusURL})
}

func (a *API) deleteJob(w http.ResponseWriter, r *http.Request) {
	a.maintenance.Lock()
	defer a.maintenance.Unlock()
	unlock := a.jobs.LockMaintenance()
	defer unlock()
	id := r.PathValue("jobId")
	job, err := a.jobs.Get(id)
	if errors.Is(err, sql.ErrNoRows) {
		writeProblem(w, http.StatusNotFound, "Job not found", "JOB_NOT_FOUND",
			"No job was found for the supplied identifier.")
		return
	}
	if err != nil {
		a.logger.Error("could not read OCR job for deletion", "job_id", id, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
			"The job could not be deleted.")
		return
	}
	switch job.Status {
	case "succeeded", "failed", "cancelled":
	default:
		writeProblem(w, http.StatusConflict, "Job is not terminal", "JOB_NOT_TERMINAL",
			"Cancel the job and wait until it reaches a terminal state before deleting it.")
		return
	}
	if err := removeUploadedJobWorkspace(a.config.DataRoot, job); err != nil {
		a.logger.Error("could not remove uploaded OCR job workspace", "job_id", id, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
			"The job data could not be deleted.")
		return
	}
	if err := a.store.DeleteTerminal(id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, http.StatusNotFound, "Job not found", "JOB_NOT_FOUND",
				"No job was found for the supplied identifier.")
			return
		}
		a.logger.Error("could not delete terminal OCR job", "job_id", id, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
			"The job could not be deleted.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) getJobResult(w http.ResponseWriter, r *http.Request) {
	a.maintenance.RLock()
	defer a.maintenance.RUnlock()
	id := r.PathValue("jobId")
	job, err := a.jobs.Get(id)
	if errors.Is(err, sql.ErrNoRows) {
		writeProblem(w, http.StatusNotFound, "Job not found", "JOB_NOT_FOUND",
			"No job was found for the supplied identifier.")
		return
	}
	if err != nil {
		a.logger.Error("could not read OCR job result", "job_id", id, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
			"The job result could not be read.")
		return
	}
	if job.Status != "succeeded" {
		writeProblem(w, http.StatusConflict, "Job result unavailable", "JOB_NOT_COMPLETED",
			"The processed files are available only after the job succeeds.")
		return
	}
	a.writeJobResultArchive(w, job)
}

func (a *API) writeJobResultArchive(w http.ResponseWriter, job Job) {
	id := job.ID
	root, err := filepath.EvalSymlinks(a.config.DataRoot)
	if err != nil {
		a.logger.Error("could not resolve data root for job result", "job_id", id, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
			"The job result could not be read.")
		return
	}
	resultDirectory, err := resolveRelative(root, job.OutputPath, isUploadedJob(job))
	if err == nil {
		resultDirectory, err = filepath.EvalSymlinks(resultDirectory)
	}
	if errors.Is(err, os.ErrNotExist) && isUploadedJob(job) {
		writeProblem(w, http.StatusGone, "Job result unavailable", "JOB_RESULT_UNAVAILABLE",
			"The uploaded job workspace is no longer present on the data volume.")
		return
	}
	if err != nil || !within(root, resultDirectory) {
		a.logger.Error("job result path is no longer valid", "job_id", id)
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
			"The job result could not be read.")
		return
	}
	info, err := os.Stat(resultDirectory)
	if err != nil || !info.IsDir() {
		a.logger.Error("job result directory is unavailable", "job_id", id, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
			"The job result could not be read.")
		return
	}
	archive, err := os.CreateTemp("", "big-batch-ocr-result-*.tar.gz")
	if err != nil {
		a.logger.Error("could not stage job result archive", "job_id", id, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
			"The job result could not be prepared.")
		return
	}
	archivePath := archive.Name()
	if err := archive.Close(); err != nil {
		_ = os.Remove(archivePath)
		a.logger.Error("could not close staged job result archive", "job_id", id, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
			"The job result could not be prepared.")
		return
	}
	defer os.Remove(archivePath)
	if err := createTarGzip(resultDirectory, archivePath); err != nil {
		a.logger.Error("could not create job result archive", "job_id", id, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
			"The job result could not be prepared.")
		return
	}
	file, err := os.Open(archivePath)
	if err != nil {
		a.logger.Error("could not open staged job result archive", "job_id", id, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
			"The job result could not be read.")
		return
	}
	defer file.Close()
	archiveInfo, err := file.Stat()
	if err != nil {
		a.logger.Error("could not inspect staged job result archive", "job_id", id, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
			"The job result could not be read.")
		return
	}
	w.Header().Set("X-Job-ID", job.ID)
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.tar.gz"`, job.ID))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", archiveInfo.Size()))
	if _, err := io.Copy(w, file); err != nil {
		a.logger.Warn("could not send job result archive", "job_id", id, "error", err)
	}
}

func writeProblem(w http.ResponseWriter, status int, title, code, detail string) {
	writeJSONContentType(w, status, problem{
		Type: "about:blank", Title: title, Status: status, Code: code, Detail: detail,
	}, "application/problem+json")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	writeJSONContentType(w, status, value, "application/json")
}

func writeJSONContentType(w http.ResponseWriter, status int, value any, contentType string) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Error("write HTTP response", "error", err)
	}
}

var _ http.Handler = (*API)(nil)
