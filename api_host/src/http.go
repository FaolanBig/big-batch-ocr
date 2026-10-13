package main

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type API struct {
	config Config
	jobs   *JobService
	store  *JobStore
	logger *slog.Logger
	mux    *http.ServeMux
}

type submitRequest struct {
	InputPath         string          `json:"inputPath"`
	OutputPath        string          `json:"outputPath"`
	Mode              string          `json:"mode"`
	CreateDestination json.RawMessage `json:"createDestination"`
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
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request submitRequest
	if err := decoder.Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid request", "INVALID_REQUEST",
			"The request body is not valid for this operation.")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeProblem(w, http.StatusBadRequest, "Invalid request", "INVALID_REQUEST",
			"The request body must contain exactly one JSON object.")
		return
	}
	if strings.TrimSpace(request.InputPath) == "" || strings.TrimSpace(request.OutputPath) == "" ||
		(request.Mode != "synchronous" && request.Mode != "asynchronous") {
		writeProblem(w, http.StatusBadRequest, "Invalid request", "INVALID_REQUEST",
			"The request body is not valid for this operation.")
		return
	}
	createDestination := false
	if len(request.CreateDestination) != 0 {
		trimmed := strings.TrimSpace(string(request.CreateDestination))
		if trimmed == "null" || json.Unmarshal(request.CreateDestination, &createDestination) != nil {
			writeProblem(w, http.StatusBadRequest, "Invalid request", "INVALID_REQUEST",
				"The request body is not valid for this operation.")
			return
		}
	}
	paths, err := ValidatePaths(a.config.DataRoot, a.config.DockerDataRoot,
		request.InputPath, request.OutputPath, createDestination)
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
	a.logger.Info("OCR job accepted", "job_id", job.ID, "mode", request.Mode)
	if request.Mode == "asynchronous" {
		w.Header().Set("Location", job.StatusURL)
		writeJSON(w, http.StatusAccepted, acknowledgement{JobID: job.ID, Status: job.Status, StatusURL: job.StatusURL})
		return
	}
	a.waitSynchronously(w, r, job.ID)
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
