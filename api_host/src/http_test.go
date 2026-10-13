package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testExecutor struct {
	run func(context.Context, Job, func(string) error, func([]byte) error) (int, error)
}

func (e testExecutor) Run(ctx context.Context, job Job, markRunning func(string) error, saveLogs func([]byte) error) (int, error) {
	return e.run(ctx, job, markRunning, saveLogs)
}

func testAPI(t *testing.T, executor testExecutor, wait time.Duration) (*API, *JobStore, func()) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "input"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "output"), 0o750); err != nil {
		t.Fatal(err)
	}
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	jobs := NewJobService(store, executor, 1, logger)
	jobs.Start()
	cfg := Config{
		APIKey: "test-key", DataRoot: root, DockerDataRoot: "/daemon/data",
		SyncWaitLimit: wait,
	}
	api := NewAPI(cfg, jobs, store, logger)
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		jobs.Shutdown(ctx)
		store.Close()
	}
	return api, store, cleanup
}

func TestAuthenticationAndHealth(t *testing.T) {
	api, _, cleanup := testAPI(t, testExecutor{run: func(context.Context, Job, func(string) error, func([]byte) error) (int, error) { return 0, nil }}, time.Second)
	defer cleanup()
	health := httptest.NewRecorder()
	api.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d", health.Code)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(`{"inputPath":"input","outputPath":"output","mode":"asynchronous"}`))
	unauthorized := httptest.NewRecorder()
	api.ServeHTTP(unauthorized, request)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	if strings.Contains(unauthorized.Body.String(), "test-key") {
		t.Fatal("credential leaked in error response")
	}
}

func TestAnonymousAccessRequiresExplicitConfiguration(t *testing.T) {
	api, _, cleanup := testAPI(t, testExecutor{run: func(context.Context, Job, func(string) error, func([]byte) error) (int, error) {
		return 0, nil
	}}, time.Second)
	defer cleanup()
	api.config.AllowAnonymous = true
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs",
		strings.NewReader(`{"inputPath":"input","outputPath":"output","mode":"asynchronous"}`))
	api.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("anonymous mode response = %d, body: %s", response.Code, response.Body.String())
	}
}

func TestAsyncJobThenStatusAndSyncSuccess(t *testing.T) {
	api, store, cleanup := testAPI(t, testExecutor{run: func(_ context.Context, job Job, markRunning func(string) error, saveLogs func([]byte) error) (int, error) {
		if err := markRunning("container-id"); err != nil {
			return 1, err
		}
		if err := saveLogs([]byte("captured test log")); err != nil {
			return 1, err
		}
		return 0, nil
	}}, time.Second)
	defer cleanup()
	asyncResponse := submitTestJob(api, `{"inputPath":"input","outputPath":"output","mode":"asynchronous"}`)
	if asyncResponse.Code != http.StatusAccepted {
		t.Fatalf("async status = %d, body: %s", asyncResponse.Code, asyncResponse.Body.String())
	}
	var accepted acknowledgement
	if err := json.Unmarshal(asyncResponse.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	status := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, accepted.StatusURL, nil)
	request.Header.Set("Authorization", "Bearer test-key")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status = httptest.NewRecorder()
		time.Sleep(10 * time.Millisecond)
		api.ServeHTTP(status, request)
		if strings.Contains(status.Body.String(), `"status":"succeeded"`) {
			break
		}
	}
	if !strings.Contains(status.Body.String(), `"status":"succeeded"`) {
		t.Fatalf("job did not succeed: %s", status.Body.String())
	}
	saved, err := store.Get(accepted.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ContainerID != "container-id" || saved.ContainerLogs != "captured test log" {
		t.Fatalf("container metadata was not persisted: %#v", saved)
	}
	if strings.Contains(status.Body.String(), "captured test log") || strings.Contains(status.Body.String(), "container-id") {
		t.Fatal("internal container details leaked through the public API")
	}
	syncResponse := submitTestJob(api, `{"inputPath":"input","outputPath":"output","mode":"synchronous"}`)
	if syncResponse.Code != http.StatusOK {
		t.Fatalf("sync status = %d, body: %s", syncResponse.Code, syncResponse.Body.String())
	}
}

func TestSynchronousTimeoutDoesNotCancelJob(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	api, store, cleanup := testAPI(t, testExecutor{run: func(ctx context.Context, job Job, markRunning func(string) error, _ func([]byte) error) (int, error) {
		if err := markRunning("container-id"); err != nil {
			return 1, err
		}
		close(started)
		select {
		case <-release:
			return 0, nil
		case <-ctx.Done():
			return 1, ctx.Err()
		}
	}}, 30*time.Millisecond)
	defer cleanup()
	response := submitTestJob(api, `{"inputPath":"input","outputPath":"output","mode":"synchronous"}`)
	<-started
	if response.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, body: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("timeout content type = %q", response.Header().Get("Content-Type"))
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		jobs, err := store.Get(mustProblemJobID(t, response))
		if err == nil && jobs.Status == "succeeded" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed-out synchronous job did not continue to completion")
}

func TestInvalidPathsNeverSubmitAndUnknownFieldsAreRejected(t *testing.T) {
	called := false
	api, _, cleanup := testAPI(t, testExecutor{run: func(context.Context, Job, func(string) error, func([]byte) error) (int, error) {
		called = true
		return 0, nil
	}}, time.Second)
	defer cleanup()
	response := submitTestJob(api, `{"inputPath":"../outside","outputPath":"output","mode":"asynchronous"}`)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid path status = %d", response.Code)
	}
	response = submitTestJob(api, `{"inputPath":"input","outputPath":"output","mode":"asynchronous","dockerOptions":["--privileged"]}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d", response.Code)
	}
	response = submitTestJob(api, `{"inputPath":"input","outputPath":"output","mode":"asynchronous","createDestination":null}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("null createDestination status = %d", response.Code)
	}
	if called {
		t.Fatal("invalid request started an OCR job")
	}
}

func TestExecutionFailureReturnsJobDetails(t *testing.T) {
	api, store, cleanup := testAPI(t, testExecutor{run: func(context.Context, Job, func(string) error, func([]byte) error) (int, error) {
		return 17, errors.New("private docker error")
	}}, time.Second)
	defer cleanup()
	response := submitTestJob(api, `{"inputPath":"input","outputPath":"output","mode":"synchronous"}`)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("failure content type = %q", response.Header().Get("Content-Type"))
	}
	if strings.Contains(response.Body.String(), "private docker error") {
		t.Fatal("internal error leaked in response")
	}
	if !strings.Contains(response.Body.String(), `"jobStatus":"failed"`) {
		t.Fatalf("failure details missing: %s", response.Body.String())
	}
	job, err := store.Get(mustProblemJobID(t, response))
	if err != nil {
		t.Fatal(err)
	}
	if job.ExitCode != nil {
		t.Fatalf("infrastructure failure should not invent an exit code: %d", *job.ExitCode)
	}
}

func TestNonzeroContainerExitIsPersistedAsFailure(t *testing.T) {
	api, store, cleanup := testAPI(t, testExecutor{run: func(context.Context, Job, func(string) error, func([]byte) error) (int, error) {
		return 17, nil
	}}, time.Second)
	defer cleanup()
	response := submitTestJob(api, `{"inputPath":"input","outputPath":"output","mode":"synchronous"}`)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body: %s", response.Code, response.Body.String())
	}
	job, err := store.Get(mustProblemJobID(t, response))
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "failed" || job.ExitCode == nil || *job.ExitCode != 17 {
		t.Fatalf("nonzero exit was not accurately persisted: %#v", job)
	}
}

func TestJobPersistsAcrossStoreReopenAndRecoveryFailsActiveState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	store, err := OpenJobStore(path)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Create("input", "output", "/host/input", "/host/output")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimNext(); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunning(job.ID, "docker-id"); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = OpenJobStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ids, err := store.InterruptedContainerIDs()
	if err != nil || len(ids) != 1 || ids[0] != "docker-id" {
		t.Fatalf("interrupted container ids = %v, err = %v", ids, err)
	}
	if err := store.RecoverInterrupted(); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.Get(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != "failed" || recovered.Error == nil || recovered.ExitCode != nil {
		t.Fatalf("unexpected recovered state: %#v", recovered)
	}
}

func submitTestJob(api *API, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer test-key")
	api.ServeHTTP(recorder, request)
	return recorder
}

func mustProblemJobID(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var value struct {
		JobID string `json:"jobId"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value.JobID
}
