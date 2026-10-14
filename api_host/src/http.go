package main

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxRequestBytes int64 = 1 << 30

type API struct {
	config Config
	jobs   *JobService
	store  *JobStore
	logger *slog.Logger
	mux    *http.ServeMux
}

type submitRequest struct {
	InputPath              string          `json:"inputPath"`
	OutputPath             string          `json:"outputPath"`
	Mode                   string          `json:"mode"`
	CreateDestination      json.RawMessage `json:"createDestination"`
	CreateDestinationValue bool            `json:"-"`
}

type acknowledgement struct {
	JobID     string `json:"jobId"`
	Status    string `json:"status"`
	StatusURL string `json:"statusUrl"`
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
	api.mux.Handle("GET /api/v1/jobs/{jobId}", api.authenticate(http.HandlerFunc(api.getJob)))
	api.mux.Handle("GET /api/v1/jobs/{jobId}/result", api.authenticate(http.HandlerFunc(api.getJobResult)))
	return api
}

func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	a.mux.ServeHTTP(w, r)
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
	request, archive, err := parseSubmitRequest(w, r)
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeProblem(w, http.StatusRequestEntityTooLarge, "Request too large", "REQUEST_TOO_LARGE",
				"The request body exceeds its size limit.")
			return
		}
		writeProblem(w, http.StatusBadRequest, "Invalid request", "INVALID_REQUEST",
			"The request body is not valid for this operation.")
		return
	}
	var uploadDirectory string
	if archive != nil {
		uploadDirectory, err = createUploadDirectory(a.config.DataRoot)
		if err != nil {
			a.logger.Error("could not create archive upload directory", "error", err)
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
		extractErr := extractTarGzip(input, uploadDirectory)
		closeErr := input.Close()
		if err := errors.Join(extractErr, closeErr); err != nil {
			writeProblem(w, http.StatusBadRequest, "Invalid archive", "INVALID_ARCHIVE",
				"The uploaded file must be a valid tar.gz archive containing regular files and directories.")
			return
		}
		relative, err := filepath.Rel(a.config.DataRoot, uploadDirectory)
		if err != nil {
			a.logger.Error("could not resolve archive upload path", "error", err)
			writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
				"The input archive could not be accepted.")
			return
		}
		request.InputPath = filepath.ToSlash(relative)
	}
	if strings.TrimSpace(request.InputPath) == "" || strings.TrimSpace(request.OutputPath) == "" ||
		(request.Mode != "synchronous" && request.Mode != "asynchronous") {
		writeProblem(w, http.StatusBadRequest, "Invalid request", "INVALID_REQUEST",
			"The request body is not valid for this operation.")
		return
	}
	paths, err := ValidatePaths(a.config.DataRoot, a.config.DockerDataRoot,
		request.InputPath, request.OutputPath, request.CreateDestinationValue)
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
	job, err := a.jobs.Submit(paths.InputPath, paths.OutputPath, paths.InputHost, paths.OutputHost)
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

func parseSubmitRequest(w http.ResponseWriter, r *http.Request) (submitRequest, *multipart.FileHeader, error) {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil && r.Header.Get("Content-Type") != "" {
		return submitRequest{}, nil, err
	}
	if contentType == "multipart/form-data" {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			return submitRequest{}, nil, err
		}
		form := r.MultipartForm
		for key := range form.Value {
			switch key {
			case "inputPath", "outputPath", "mode", "createDestination":
			default:
				return submitRequest{}, nil, errors.New("unknown multipart field")
			}
			if len(form.Value[key]) != 1 {
				return submitRequest{}, nil, errors.New("multipart field must appear once")
			}
		}
		for key := range form.File {
			if key != "inputArchive" || len(form.File[key]) != 1 {
				return submitRequest{}, nil, errors.New("unknown or repeated multipart file field")
			}
		}
		inputPath, err := multipartTextField(r, "inputPath", false)
		if err != nil {
			return submitRequest{}, nil, err
		}
		outputPath, err := multipartTextField(r, "outputPath", true)
		if err != nil {
			return submitRequest{}, nil, err
		}
		mode, err := multipartTextField(r, "mode", true)
		if err != nil {
			return submitRequest{}, nil, err
		}
		createDestination, err := multipartTextField(r, "createDestination", false)
		if err != nil {
			return submitRequest{}, nil, err
		}
		create := false
		if createDestination != "" {
			switch strings.ToLower(createDestination) {
			case "true":
				create = true
			case "false":
			default:
				return submitRequest{}, nil, errors.New("createDestination must be true or false")
			}
		}
		var archive *multipart.FileHeader
		if files := form.File["inputArchive"]; len(files) == 1 {
			archive = files[0]
			if !strings.HasSuffix(strings.ToLower(archive.Filename), ".tar.gz") {
				return submitRequest{}, nil, errors.New("inputArchive must have a .tar.gz filename")
			}
			if inputPath != "" {
				return submitRequest{}, nil, errors.New("inputPath and inputArchive are mutually exclusive")
			}
		}
		if archive == nil && inputPath == "" {
			return submitRequest{}, nil, errors.New("inputPath is required without inputArchive")
		}
		return submitRequest{
			InputPath: inputPath, OutputPath: outputPath, Mode: mode,
			CreateDestinationValue: create,
		}, archive, nil
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
	return request, nil, nil
}

func multipartTextField(r *http.Request, key string, required bool) (string, error) {
	values := r.MultipartForm.Value[key]
	if len(values) == 0 && !required {
		return "", nil
	}
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return "", errors.New("multipart text field is missing or repeated")
	}
	return values[0], nil
}

func createUploadDirectory(dataRoot string) (string, error) {
	root, err := filepath.EvalSymlinks(dataRoot)
	if err != nil {
		return "", fmt.Errorf("resolve data root: %w", err)
	}
	parent := filepath.Join(root, ".api-host")
	if err := os.Mkdir(parent, 0o750); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create private data directory: %w", err)
	}
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("private data directory is not a real directory")
	}
	uploadRoot := filepath.Join(parent, "uploads")
	if err := os.Mkdir(uploadRoot, 0o750); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create upload directory: %w", err)
	}
	info, err = os.Lstat(uploadRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("upload directory is not a real directory")
	}
	directory, err := os.MkdirTemp(uploadRoot, "job-")
	if err != nil {
		return "", fmt.Errorf("create job upload directory: %w", err)
	}
	if err := os.Chmod(directory, 0o750); err != nil {
		_ = os.RemoveAll(directory)
		return "", fmt.Errorf("set upload directory permissions: %w", err)
	}
	return directory, nil
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
			writeJSON(w, http.StatusOK, map[string]any{
				"jobId": job.ID, "status": job.Status, "inputPath": job.InputPath,
				"outputPath": job.OutputPath, "createdAt": job.CreatedAt,
				"startedAt": job.StartedAt, "completedAt": job.CompletedAt,
				"exitCode": job.ExitCode, "statusUrl": job.StatusURL,
			})
			return
		case "failed":
			writeJSONContentType(w, http.StatusInternalServerError, problem{
				Type: "about:blank", Title: "OCR execution failed", Status: http.StatusInternalServerError,
				Code: "OCR_EXECUTION_FAILED", Detail: "The OCR container exited unsuccessfully.",
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
			if job.Status == "succeeded" || job.Status == "failed" {
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

func (a *API) getJobResult(w http.ResponseWriter, r *http.Request) {
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
	root, err := filepath.EvalSymlinks(a.config.DataRoot)
	if err != nil {
		a.logger.Error("could not resolve data root for job result", "job_id", id, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "INTERNAL_ERROR",
			"The job result could not be read.")
		return
	}
	resultDirectory, err := resolveRelative(root, job.OutputPath)
	if err == nil {
		resultDirectory, err = filepath.EvalSymlinks(resultDirectory)
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
