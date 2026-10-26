package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
		APIKey: "test-key", DataRoot: root, DockerDataRoot: root,
		AllowDirectoryAccess: true, OCRLanguage: "eng", OCROptimize: 3, SyncWaitLimit: wait,
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

func TestRequestLoggingIncludesOutcomeWithoutQueryString(t *testing.T) {
	api, _, cleanup := testAPI(t, testExecutor{run: func(context.Context, Job, func(string) error, func([]byte) error) (int, error) {
		return 0, nil
	}}, time.Second)
	defer cleanup()

	var logs bytes.Buffer
	api.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/private?token=do-not-log", nil)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("response status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	output := logs.String()
	for _, expected := range []string{`"method":"GET"`, `"path":"/api/v1/jobs/private"`, `"status":401`} {
		if !strings.Contains(output, expected) {
			t.Errorf("request log missing %s: %s", expected, output)
		}
	}
	if strings.Contains(output, "do-not-log") {
		t.Fatal("request log included the query string")
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
	if saved.OCRLanguage != "eng" || saved.OCROptimize != 3 {
		t.Fatalf("default OCR options were not persisted: %q/%d", saved.OCRLanguage, saved.OCROptimize)
	}
	if strings.Contains(status.Body.String(), "captured test log") || strings.Contains(status.Body.String(), "container-id") {
		t.Fatal("internal container details leaked through the public API")
	}
	syncResponse := submitTestJob(api, `{"inputPath":"input","outputPath":"output","mode":"synchronous"}`)
	if syncResponse.Code != http.StatusOK {
		t.Fatalf("sync status = %d, body: %s", syncResponse.Code, syncResponse.Body.String())
	}
	if got := syncResponse.Header().Get("Content-Type"); got != "application/gzip" {
		t.Fatalf("sync content type = %q", got)
	}
	if syncResponse.Header().Get("X-Job-ID") == "" {
		t.Fatal("sync result did not include the job ID header")
	}
}

func TestSubmitJobAcceptsValidatedOCROverrides(t *testing.T) {
	api, store, cleanup := testAPI(t, testExecutor{run: func(_ context.Context, job Job, markRunning func(string) error, _ func([]byte) error) (int, error) {
		if err := markRunning("container-id"); err != nil {
			return 1, err
		}
		return 0, nil
	}}, time.Second)
	defer cleanup()

	response := submitTestJob(api, `{"inputPath":"input","outputPath":"output","mode":"asynchronous","language":"deu+eng","optimize":0}`)
	if response.Code != http.StatusAccepted {
		t.Fatalf("submission status = %d, body: %s", response.Code, response.Body.String())
	}
	var accepted acknowledgement
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	job, err := store.Get(accepted.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.OCRLanguage != "deu+eng" || job.OCROptimize != 0 {
		t.Fatalf("stored OCR options = %q/%d", job.OCRLanguage, job.OCROptimize)
	}

	for _, body := range []string{
		`{"inputPath":"input","outputPath":"output","mode":"asynchronous","language":"eng;--side-channel"}`,
		`{"inputPath":"input","outputPath":"output","mode":"asynchronous","optimize":4}`,
	} {
		response := submitTestJob(api, body)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "INVALID_OCR_OPTIONS") {
			t.Fatalf("invalid OCR options response = %d, body: %s", response.Code, response.Body.String())
		}
	}
	for _, body := range []string{
		`{"inputPath":"input","outputPath":"output","mode":"asynchronous","language":null}`,
		`{"inputPath":"input","outputPath":"output","mode":"asynchronous","optimize":null}`,
	} {
		response := submitTestJob(api, body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("null OCR option response = %d, body: %s", response.Code, response.Body.String())
		}
	}
}

func TestMultipartArchiveJobAndDownloadResult(t *testing.T) {
	api, _, cleanup := testAPI(t, testExecutor{run: func(_ context.Context, job Job, markRunning func(string) error, _ func([]byte) error) (int, error) {
		if filepath.Base(filepath.Dir(job.InputHost)) != job.ID || filepath.Base(filepath.Dir(job.OutputHost)) != job.ID {
			return 1, errors.New("uploaded job paths are not isolated by job ID")
		}
		input, err := os.ReadFile(filepath.Join(job.InputHost, "invoices", "invoice.pdf"))
		if err != nil {
			return 1, err
		}
		if string(input) != "source pdf" {
			return 1, errors.New("unexpected uploaded input")
		}
		if err := markRunning("container-id"); err != nil {
			return 1, err
		}
		return 0, os.WriteFile(filepath.Join(job.OutputHost, "recognized.txt"), []byte("recognized text"), 0o640)
	}}, time.Second)
	defer cleanup()
	if err := os.WriteFile(filepath.Join(api.config.DataRoot, "output", "previous-job.txt"), []byte("keep me"), 0o640); err != nil {
		t.Fatal(err)
	}

	body, contentType := multipartArchiveRequest(t, testArchive(t, "invoices/invoice.pdf", "source pdf"), true)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Authorization", "Bearer "+api.config.APIKey)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("submission status = %d, body: %s", response.Code, response.Body.String())
	}
	jobID := response.Header().Get("X-Job-ID")
	if jobID == "" {
		t.Fatal("synchronous result did not include an X-Job-ID header")
	}
	if got := response.Header().Get("Content-Type"); got != "application/gzip" {
		t.Fatalf("synchronous result content type = %q", got)
	}
	if got := response.Header().Get("Content-Disposition"); !strings.Contains(got, jobID+".tar.gz") {
		t.Fatalf("synchronous result disposition = %q", got)
	}
	gzipReader, err := gzip.NewReader(bytes.NewReader(response.Body.Bytes()))
	if err != nil {
		t.Fatalf("synchronous response is not a gzip archive: %v", err)
	}
	tarReader := tar.NewReader(gzipReader)
	header, err := tarReader.Next()
	if err != nil {
		t.Fatalf("synchronous archive has no result entry: %v", err)
	}
	if header.Name != "recognized.txt" {
		t.Fatalf("synchronous archive filename = %q", header.Name)
	}
	content, err := io.ReadAll(tarReader)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "recognized text" {
		t.Fatalf("synchronous archive file content = %q", content)
	}

	download := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+jobID+"/result", nil)
	request.Header.Set("Authorization", "Bearer "+api.config.APIKey)
	api.ServeHTTP(download, request)
	if download.Code != http.StatusOK {
		t.Fatalf("download status = %d, body: %s", download.Code, download.Body.String())
	}
	if got := download.Header().Get("Content-Type"); got != "application/gzip" {
		t.Fatalf("download content type = %q", got)
	}
	if got := download.Header().Get("Content-Disposition"); !strings.Contains(got, jobID+".tar.gz") {
		t.Fatalf("download disposition = %q", got)
	}
	gzipReader, err = gzip.NewReader(bytes.NewReader(download.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	tarReader = tar.NewReader(gzipReader)
	header, err = tarReader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if header.Name != "recognized.txt" {
		t.Fatalf("downloaded filename = %q", header.Name)
	}
	content, err = io.ReadAll(tarReader)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "recognized text" {
		t.Fatalf("downloaded file content = %q", content)
	}
	jobDirectory := filepath.Join(api.config.DataRoot, ".api-host", "jobs", jobID)
	if _, err := os.Stat(jobDirectory); err != nil {
		t.Fatalf("uploaded job workspace was removed by download: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(api.config.DataRoot, "output", "previous-job.txt")); err != nil {
		t.Fatalf("existing shared output was unexpectedly removed: %v", err)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+jobID+"/result", nil)
	request.Header.Set("Authorization", "Bearer "+api.config.APIKey)
	repeatedDownload := httptest.NewRecorder()
	api.ServeHTTP(repeatedDownload, request)
	if repeatedDownload.Code != http.StatusOK {
		t.Fatalf("repeated download status = %d, body: %s", repeatedDownload.Code, repeatedDownload.Body.String())
	}
	if deleted := deleteTestJob(api, jobID); deleted.Code != http.StatusNoContent {
		t.Fatalf("explicit job deletion status = %d, body: %s", deleted.Code, deleted.Body.String())
	}
	if _, err := os.Stat(jobDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("uploaded workspace remains after explicit job deletion: err=%v", err)
	}
}

func TestMultipartArchiveAcceptsOCROverrides(t *testing.T) {
	received := make(chan OCROptions, 1)
	api, _, cleanup := testAPI(t, testExecutor{run: func(_ context.Context, job Job, markRunning func(string) error, _ func([]byte) error) (int, error) {
		received <- OCROptions{Language: job.OCRLanguage, Optimize: job.OCROptimize}
		if err := markRunning("container-id"); err != nil {
			return 1, err
		}
		return 0, nil
	}}, time.Second)
	defer cleanup()

	body, contentType := multipartArchiveRequestWithOCR(t, testArchive(t, "invoice.pdf", "source"), "asynchronous", "deu+eng", "0")
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Authorization", "Bearer "+api.config.APIKey)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("multipart submission status = %d, body: %s", response.Code, response.Body.String())
	}
	select {
	case options := <-received:
		if options != (OCROptions{Language: "deu+eng", Optimize: 0}) {
			t.Fatalf("executor received OCR options = %#v", options)
		}
	case <-time.After(time.Second):
		t.Fatal("OCR job was not executed")
	}
}

func TestMultipartArchiveContentsFieldAcceptsRawTarGzip(t *testing.T) {
	api, _, cleanup := testAPI(t, testExecutor{run: func(context.Context, Job, func(string) error, func([]byte) error) (int, error) {
		return 0, nil
	}}, time.Second)
	defer cleanup()
	api.config.AllowDirectoryAccess = false

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("mode", "asynchronous"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("inputArchiveContents", string(testArchive(t, "invoice.pdf", "source"))); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+api.config.APIKey)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("archive contents submission status = %d, body: %s", response.Code, response.Body.String())
	}

	var accepted acknowledgement
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	uploadedFile := filepath.Join(api.config.DataRoot, ".api-host", "jobs", accepted.JobID, "input", "invoice.pdf")
	if content, err := os.ReadFile(uploadedFile); err != nil || string(content) != "source" {
		t.Fatalf("uploaded archive contents were not extracted: content=%q err=%v", content, err)
	}
}

func TestMultipartArchiveContentsBase64FieldAcceptsEncodedTarGzip(t *testing.T) {
	api, _, cleanup := testAPI(t, testExecutor{run: func(context.Context, Job, func(string) error, func([]byte) error) (int, error) {
		return 0, nil
	}}, time.Second)
	defer cleanup()
	api.config.AllowDirectoryAccess = false

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("mode", "asynchronous"); err != nil {
		t.Fatal(err)
	}
	archive := testArchive(t, "invoice.pdf", "source")
	if err := writer.WriteField("inputArchiveContentsBase64", base64.StdEncoding.EncodeToString(archive)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+api.config.APIKey)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("base64 archive submission status = %d, body: %s", response.Code, response.Body.String())
	}

	var accepted acknowledgement
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	uploadedFile := filepath.Join(api.config.DataRoot, ".api-host", "jobs", accepted.JobID, "input", "invoice.pdf")
	if content, err := os.ReadFile(uploadedFile); err != nil || string(content) != "source" {
		t.Fatalf("base64 archive contents were not decoded and extracted: content=%q err=%v", content, err)
	}
}

func TestMultipartArchiveContentsBase64FieldRejectsInvalidBase64(t *testing.T) {
	api, _, cleanup := testAPI(t, testExecutor{run: func(context.Context, Job, func(string) error, func([]byte) error) (int, error) {
		return 0, nil
	}}, time.Second)
	defer cleanup()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("mode", "asynchronous"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("inputArchiveContentsBase64", "not base64!"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+api.config.APIKey)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid base64 archive submission status = %d, body: %s", response.Code, response.Body.String())
	}
}

func TestDirectorySubmissionsCanBeDisabledWithoutDisablingArchiveUploads(t *testing.T) {
	api, _, cleanup := testAPI(t, testExecutor{run: func(context.Context, Job, func(string) error, func([]byte) error) (int, error) {
		return 0, nil
	}}, time.Second)
	defer cleanup()
	api.config.AllowDirectoryAccess = false

	directoryResponse := submitTestJob(api, `{"inputPath":"input","outputPath":"output","mode":"synchronous"}`)
	if directoryResponse.Code != http.StatusForbidden {
		t.Fatalf("directory submission status = %d, body: %s", directoryResponse.Code, directoryResponse.Body.String())
	}
	if !strings.Contains(directoryResponse.Body.String(), `"code":"DIRECTORY_ACCESS_DISABLED"`) {
		t.Fatalf("directory submission response lacks disable reason: %s", directoryResponse.Body.String())
	}

	directoryBody, directoryContentType := multipartArchiveRequest(t, nil, false)
	directoryRequest := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", directoryBody)
	directoryRequest.Header.Set("Content-Type", directoryContentType)
	directoryRequest.Header.Set("Authorization", "Bearer "+api.config.APIKey)
	directoryMultipartResponse := httptest.NewRecorder()
	api.ServeHTTP(directoryMultipartResponse, directoryRequest)
	if directoryMultipartResponse.Code != http.StatusForbidden {
		t.Fatalf("multipart directory submission status = %d, body: %s", directoryMultipartResponse.Code, directoryMultipartResponse.Body.String())
	}

	body, contentType := multipartArchiveRequest(t, testArchive(t, "invoice.pdf", "source pdf"), true)
	uploadRequest := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", body)
	uploadRequest.Header.Set("Content-Type", contentType)
	uploadRequest.Header.Set("Authorization", "Bearer "+api.config.APIKey)
	uploadResponse := httptest.NewRecorder()
	api.ServeHTTP(uploadResponse, uploadRequest)
	if uploadResponse.Code != http.StatusOK {
		t.Fatalf("archive upload status = %d, body: %s", uploadResponse.Code, uploadResponse.Body.String())
	}
}

func TestCancelQueuedUploadedJobPreservesWorkspaceForExplicitCleanup(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var executions int
	api, store, cleanup := testAPI(t, testExecutor{run: func(ctx context.Context, job Job, markRunning func(string) error, _ func([]byte) error) (int, error) {
		executions++
		if err := markRunning("container-" + job.ID); err != nil {
			return 1, err
		}
		if executions == 1 {
			close(started)
			select {
			case <-release:
				return 0, nil
			case <-ctx.Done():
				return 1, ctx.Err()
			}
		}
		return 0, nil
	}}, time.Second)
	defer cleanup()
	first := submitTestJob(api, `{"inputPath":"input","outputPath":"output","mode":"asynchronous"}`)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first submission status = %d, body: %s", first.Code, first.Body.String())
	}
	<-started
	body, contentType := multipartArchiveRequestWithMode(t, testArchive(t, "invoice.pdf", "source"), true, "asynchronous")
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Authorization", "Bearer "+api.config.APIKey)
	acceptedResponse := httptest.NewRecorder()
	api.ServeHTTP(acceptedResponse, request)
	if acceptedResponse.Code != http.StatusAccepted {
		t.Fatalf("uploaded submission status = %d, body: %s", acceptedResponse.Code, acceptedResponse.Body.String())
	}
	var accepted acknowledgement
	if err := json.Unmarshal(acceptedResponse.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(api.config.DataRoot, ".api-host", "jobs", accepted.JobID)
	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("uploaded workspace was not created: %v", err)
	}
	cancelResponse := cancelTestJob(api, accepted.JobID)
	if cancelResponse.Code != http.StatusOK || !strings.Contains(cancelResponse.Body.String(), `"status":"cancelled"`) {
		t.Fatalf("queued cancellation status = %d, body: %s", cancelResponse.Code, cancelResponse.Body.String())
	}
	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("cancelled upload workspace was removed automatically: err=%v", err)
	}
	cancelled, err := store.Get(accepted.JobID)
	if err != nil || cancelled.Status != "cancelled" || cancelled.CompletedAt == nil {
		t.Fatalf("queued cancellation was not persisted: job=%#v err=%v", cancelled, err)
	}
	if executions != 1 {
		t.Fatalf("executor ran %d jobs, want only the blocker", executions)
	}
	if deleted := deleteTestJob(api, accepted.JobID); deleted.Code != http.StatusNoContent {
		t.Fatalf("explicit job deletion status = %d, body: %s", deleted.Code, deleted.Body.String())
	}
	if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled workspace remains after explicit deletion: err=%v", err)
	}
	close(release)
}

func TestCancelRunningUploadedJobStopsWorkAndPreservesWorkspace(t *testing.T) {
	started := make(chan struct{})
	api, store, cleanup := testAPI(t, testExecutor{run: func(ctx context.Context, _ Job, markRunning func(string) error, _ func([]byte) error) (int, error) {
		if err := markRunning("managed-container"); err != nil {
			return 1, err
		}
		close(started)
		<-ctx.Done()
		return 1, ctx.Err()
	}}, time.Second)
	defer cleanup()
	body, contentType := multipartArchiveRequestWithMode(t, testArchive(t, "invoice.pdf", "source"), true, "asynchronous")
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Authorization", "Bearer "+api.config.APIKey)
	acceptedResponse := httptest.NewRecorder()
	api.ServeHTTP(acceptedResponse, request)
	if acceptedResponse.Code != http.StatusAccepted {
		t.Fatalf("uploaded submission status = %d, body: %s", acceptedResponse.Code, acceptedResponse.Body.String())
	}
	var accepted acknowledgement
	if err := json.Unmarshal(acceptedResponse.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	<-started
	workspace := filepath.Join(api.config.DataRoot, ".api-host", "jobs", accepted.JobID)
	response := cancelTestJob(api, accepted.JobID)
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"status":"cancelling"`) {
		t.Fatalf("running cancellation status = %d, body: %s", response.Code, response.Body.String())
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		job, err := store.Get(accepted.JobID)
		_, statErr := os.Stat(workspace)
		if err == nil && job.Status == "cancelled" && statErr == nil {
			if job.CompletedAt == nil || job.Error == nil || job.Error.Code != "JOB_CANCELLED" {
				t.Fatalf("cancelled outcome was not fully recorded: %#v", job)
			}
			if deleted := deleteTestJob(api, accepted.JobID); deleted.Code != http.StatusNoContent {
				t.Fatalf("explicit job deletion status = %d, body: %s", deleted.Code, deleted.Body.String())
			}
			if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("cancelled workspace remains after explicit deletion: err=%v", err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	job, err := store.Get(accepted.JobID)
	t.Fatalf("running uploaded job did not finish cancellation while preserving data: job=%#v err=%v", job, err)
}

func TestDeleteTerminalUploadedJobRemovesWorkspaceAndMetadata(t *testing.T) {
	api, store, cleanup := testAPI(t, testExecutor{run: func(_ context.Context, job Job, markRunning func(string) error, _ func([]byte) error) (int, error) {
		if err := markRunning("container-id"); err != nil {
			return 1, err
		}
		return 0, os.WriteFile(filepath.Join(job.OutputHost, "recognized.txt"), []byte("result"), 0o640)
	}}, time.Second)
	defer cleanup()
	body, contentType := multipartArchiveRequestWithMode(t, testArchive(t, "invoice.pdf", "source"), true, "asynchronous")
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Authorization", "Bearer "+api.config.APIKey)
	acceptedResponse := httptest.NewRecorder()
	api.ServeHTTP(acceptedResponse, request)
	if acceptedResponse.Code != http.StatusAccepted {
		t.Fatalf("uploaded submission status = %d, body: %s", acceptedResponse.Code, acceptedResponse.Body.String())
	}
	var accepted acknowledgement
	if err := json.Unmarshal(acceptedResponse.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		job, err := store.Get(accepted.JobID)
		if err == nil && job.Status == "succeeded" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	job, err := store.Get(accepted.JobID)
	if err != nil || job.Status != "succeeded" {
		t.Fatalf("uploaded job did not succeed: job=%#v err=%v", job, err)
	}
	workspace := filepath.Join(api.config.DataRoot, ".api-host", "jobs", accepted.JobID)
	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("uploaded job workspace is missing before deletion: %v", err)
	}
	response := deleteTestJob(api, accepted.JobID)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body: %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("uploaded job workspace remains after deletion: err=%v", err)
	}
	if _, err := store.Get(accepted.JobID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("job metadata remains after deletion: err=%v", err)
	}
	status := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, accepted.StatusURL, nil)
	request.Header.Set("Authorization", "Bearer "+api.config.APIKey)
	api.ServeHTTP(status, request)
	if status.Code != http.StatusNotFound {
		t.Fatalf("deleted job status endpoint returned %d, body: %s", status.Code, status.Body.String())
	}
}

func TestDeleteActiveJobRequiresCancellationFirst(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	api, store, cleanup := testAPI(t, testExecutor{run: func(ctx context.Context, _ Job, markRunning func(string) error, _ func([]byte) error) (int, error) {
		if err := markRunning("blocker-container"); err != nil {
			return 1, err
		}
		close(started)
		select {
		case <-release:
			return 0, nil
		case <-ctx.Done():
			return 1, ctx.Err()
		}
	}}, time.Second)
	defer cleanup()
	firstResponse := submitTestJob(api, `{"inputPath":"input","outputPath":"output","mode":"asynchronous"}`)
	if firstResponse.Code != http.StatusAccepted {
		t.Fatalf("first submission status = %d, body: %s", firstResponse.Code, firstResponse.Body.String())
	}
	<-started
	queuedResponse := submitTestJob(api, `{"inputPath":"input","outputPath":"output","mode":"asynchronous"}`)
	var queued acknowledgement
	if err := json.Unmarshal(queuedResponse.Body.Bytes(), &queued); err != nil {
		t.Fatal(err)
	}
	response := deleteTestJob(api, queued.JobID)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"JOB_NOT_TERMINAL"`) {
		t.Fatalf("active job delete status = %d, body: %s", response.Code, response.Body.String())
	}
	if _, err := store.Get(queued.JobID); err != nil {
		t.Fatalf("active job metadata was removed: %v", err)
	}
	close(release)
}

func TestDeleteSharedDirectoryJobLeavesInputAndOutputDirectories(t *testing.T) {
	api, _, cleanup := testAPI(t, testExecutor{run: func(_ context.Context, job Job, markRunning func(string) error, _ func([]byte) error) (int, error) {
		if err := markRunning("container-id"); err != nil {
			return 1, err
		}
		return 0, os.WriteFile(filepath.Join(job.OutputHost, "result.txt"), []byte("keep"), 0o640)
	}}, time.Second)
	defer cleanup()
	response := submitTestJob(api, `{"inputPath":"input","outputPath":"output","mode":"synchronous"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("submission status = %d, body: %s", response.Code, response.Body.String())
	}
	jobID := response.Header().Get("X-Job-ID")
	if jobID == "" {
		t.Fatal("synchronous response did not include an X-Job-ID header")
	}
	if deleted := deleteTestJob(api, jobID); deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body: %s", deleted.Code, deleted.Body.String())
	}
	for _, name := range []string{"input", "output"} {
		if info, err := os.Stat(filepath.Join(api.config.DataRoot, name)); err != nil || !info.IsDir() {
			t.Fatalf("shared %s directory was removed: info=%v err=%v", name, info, err)
		}
	}
	if content, err := os.ReadFile(filepath.Join(api.config.DataRoot, "output", "result.txt")); err != nil || string(content) != "keep" {
		t.Fatalf("shared output data was removed: content=%q err=%v", content, err)
	}
}

func TestBulkCleanupUploadsRemovesOnlyTerminalUploadedJobs(t *testing.T) {
	api, store, cleanup := testAPI(t, testExecutor{run: func(context.Context, Job, func(string) error, func([]byte) error) (int, error) {
		return 0, nil
	}}, time.Second)
	defer cleanup()
	workspace, err := createJobWorkspace(api.config.DataRoot, "job_uploaded_cleanup")
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateWithID(
		"job_uploaded_cleanup",
		".api-host/jobs/job_uploaded_cleanup/input",
		".api-host/jobs/job_uploaded_cleanup/output",
		filepath.Join(workspace, "input"),
		filepath.Join(workspace, "output"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE jobs SET status='cancelled',completed_at=? WHERE id=?`,
		formatDBTime(time.Now().UTC()), job.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(api.config.DataRoot, "output", "keep.txt"), []byte("keep"), 0o640); err != nil {
		t.Fatal(err)
	}
	response := cleanupJobsTestRequest(api, `{"scope":"uploads"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("cleanup status = %d, body: %s", response.Code, response.Body.String())
	}
	var result cleanupResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Scope != "uploads" || result.JobsDeleted != 1 {
		t.Fatalf("unexpected cleanup result: %#v", result)
	}
	if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("uploaded workspace still exists: %v", err)
	}
	if _, err := store.Get(job.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("uploaded job metadata remains: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(api.config.DataRoot, "output", "keep.txt")); err != nil || string(content) != "keep" {
		t.Fatalf("uploads cleanup touched shared output: content=%q err=%v", content, err)
	}
}

func TestBulkCleanupUploadsIsIdempotentWhenWorkspaceParentsAreMissing(t *testing.T) {
	api, store, cleanup := testAPI(t, testExecutor{run: func(context.Context, Job, func(string) error, func([]byte) error) (int, error) {
		return 0, nil
	}}, time.Second)
	defer cleanup()
	job, err := store.CreateWithID(
		"job_removed_upload_workspace",
		".api-host/jobs/job_removed_upload_workspace/input",
		".api-host/jobs/job_removed_upload_workspace/output",
		"/daemon/data/.api-host/jobs/job_removed_upload_workspace/input",
		"/daemon/data/.api-host/jobs/job_removed_upload_workspace/output",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE jobs SET status='cancelled',completed_at=? WHERE id=?`,
		formatDBTime(time.Now().UTC()), job.ID); err != nil {
		t.Fatal(err)
	}
	response := cleanupJobsTestRequest(api, `{"scope":"uploads"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("cleanup status = %d, body: %s", response.Code, response.Body.String())
	}
	if _, err := store.Get(job.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("job metadata remains after idempotent cleanup: %v", err)
	}
}

func TestBulkCleanupAllIgnoresActiveAndProtectsSharedPaths(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var runs int
	api, store, cleanup := testAPI(t, testExecutor{run: func(ctx context.Context, _ Job, markRunning func(string) error, _ func([]byte) error) (int, error) {
		runs++
		if err := markRunning("active-container"); err != nil {
			return 1, err
		}
		if runs > 1 {
			return 0, nil
		}
		close(started)
		select {
		case <-release:
			return 0, nil
		case <-ctx.Done():
			return 1, ctx.Err()
		}
	}}, time.Second)
	defer cleanup()
	activeResponse := submitTestJob(api, `{"inputPath":"input","outputPath":"output","mode":"asynchronous"}`)
	if activeResponse.Code != http.StatusAccepted {
		t.Fatalf("active submission status = %d, body: %s", activeResponse.Code, activeResponse.Body.String())
	}
	<-started
	queuedResponse := submitTestJob(api, `{"inputPath":"input","outputPath":"output","mode":"asynchronous"}`)
	var queued acknowledgement
	if err := json.Unmarshal(queuedResponse.Body.Bytes(), &queued); err != nil {
		t.Fatal(err)
	}
	finishedInput := filepath.Join(api.config.DataRoot, "finished-input")
	finishedOutput := filepath.Join(api.config.DataRoot, "finished-output")
	for _, directory := range []string{finishedInput, finishedOutput} {
		if err := os.Mkdir(directory, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "marker.txt"), []byte("remove"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	insertJob := func(id, status, input, output string) {
		t.Helper()
		_, err := store.db.Exec(`INSERT INTO jobs(id,status,input_path,output_path,input_host,output_host,created_at,completed_at)
			VALUES(?,?,?,?,?,?,?,?)`,
			id, status, input, output, filepath.Join(api.config.DataRoot, input),
			filepath.Join(api.config.DataRoot, output), formatDBTime(time.Now().UTC()), formatDBTime(time.Now().UTC()))
		if err != nil {
			t.Fatal(err)
		}
	}
	insertJob("job_finished_cleanup", "succeeded", "finished-input", "finished-output")
	insertJob("job_overlaps_active", "failed", "input", "output")
	response := cleanupJobsTestRequest(api, `{"scope":"all"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("cleanup status = %d, body: %s", response.Code, response.Body.String())
	}
	var result cleanupResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.JobsDeleted != 1 || result.ActiveJobsSkipped != 2 || result.JobsSkippedForActiveData != 1 {
		t.Fatalf("unexpected cleanup result: %#v", result)
	}
	for _, directory := range []string{finishedInput, finishedOutput} {
		if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("terminal job directory still exists: %s err=%v", directory, err)
		}
	}
	for _, id := range []string{queued.JobID, "job_overlaps_active"} {
		if _, err := store.Get(id); err != nil {
			t.Fatalf("ignored job %s metadata was removed: %v", id, err)
		}
	}
	for _, path := range []string{"input", "output"} {
		if info, err := os.Stat(filepath.Join(api.config.DataRoot, path)); err != nil || !info.IsDir() {
			t.Fatalf("active job directory %s was removed: info=%v err=%v", path, info, err)
		}
	}
	close(release)
}

func TestBulkCleanupAllIgnoresActiveJobWithUnresolvablePaths(t *testing.T) {
	api, store, cleanup := testAPI(t, testExecutor{run: func(context.Context, Job, func(string) error, func([]byte) error) (int, error) {
		return 0, nil
	}}, time.Second)
	defer cleanup()
	_, err := store.db.Exec(`INSERT INTO jobs(id,status,input_path,output_path,input_host,output_host,created_at)
		VALUES(?,?,?,?,?,?,?)`, "job_unsafe_active", "running", "../outside", "output",
		"/outside", filepath.Join(api.config.DataRoot, "output"), formatDBTime(time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.db.Exec(`INSERT INTO jobs(id,status,input_path,output_path,input_host,output_host,created_at,completed_at)
		VALUES(?,?,?,?,?,?,?,?)`, "job_terminal_protected", "succeeded", "input", "output",
		filepath.Join(api.config.DataRoot, "input"), filepath.Join(api.config.DataRoot, "output"),
		formatDBTime(time.Now().UTC()), formatDBTime(time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	response := cleanupJobsTestRequest(api, `{"scope":"all"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("cleanup status = %d, body: %s", response.Code, response.Body.String())
	}
	var result cleanupResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ActiveJobsSkipped != 1 || result.JobsSkippedForActiveData != 1 {
		t.Fatalf("unexpected cleanup result: %#v", result)
	}
	for _, id := range []string{"job_unsafe_active", "job_terminal_protected"} {
		if _, err := store.Get(id); err != nil {
			t.Fatalf("protected job %s was deleted: %v", id, err)
		}
	}
}

func TestMultipartArchiveRejectsTraversal(t *testing.T) {
	called := false
	api, _, cleanup := testAPI(t, testExecutor{run: func(context.Context, Job, func(string) error, func([]byte) error) (int, error) {
		called = true
		return 0, nil
	}}, time.Second)
	defer cleanup()
	body, contentType := multipartArchiveRequest(t, testArchive(t, "../outside.txt", "unsafe"), true)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Authorization", "Bearer "+api.config.APIKey)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"INVALID_ARCHIVE"`) {
		t.Fatalf("traversal archive response = %d, body: %s", response.Code, response.Body.String())
	}
	if called {
		t.Fatal("unsafe archive started an OCR job")
	}
}

func TestJobResultRequiresSuccessfulJob(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	api, _, cleanup := testAPI(t, testExecutor{run: func(ctx context.Context, _ Job, markRunning func(string) error, _ func([]byte) error) (int, error) {
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
	}}, time.Second)
	defer cleanup()
	response := submitTestJob(api, `{"inputPath":"input","outputPath":"output","mode":"asynchronous"}`)
	var accepted acknowledgement
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	<-started
	download := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, accepted.StatusURL+"/result", nil)
	request.Header.Set("Authorization", "Bearer "+api.config.APIKey)
	api.ServeHTTP(download, request)
	if download.Code != http.StatusConflict || !strings.Contains(download.Body.String(), `"code":"JOB_NOT_COMPLETED"`) {
		t.Fatalf("incomplete result response = %d, body: %s", download.Code, download.Body.String())
	}
	close(release)
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

func TestFailedJobCanBeResumedWithSameIDAndOCRSettings(t *testing.T) {
	var attempts atomic.Int32
	api, store, cleanup := testAPI(t, testExecutor{run: func(_ context.Context, job Job, markRunning func(string) error, saveLogs func([]byte) error) (int, error) {
		attempt := attempts.Add(1)
		if err := markRunning("container-id"); err != nil {
			return 1, err
		}
		if attempt == 1 {
			if err := saveLogs([]byte("first attempt")); err != nil {
				return 1, err
			}
			return 1, errors.New("first attempt failed")
		}
		if job.OCRLanguage != "deu+eng" || job.OCROptimize != 0 {
			return 1, errors.New("resume lost OCR settings")
		}
		return 0, nil
	}}, time.Second)
	defer cleanup()

	response := submitTestJob(api, `{"inputPath":"input","outputPath":"output","mode":"asynchronous","language":"deu+eng","optimize":0}`)
	if response.Code != http.StatusAccepted {
		t.Fatalf("submission status = %d, body: %s", response.Code, response.Body.String())
	}
	var accepted acknowledgement
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	waitForJobStatus(t, store, accepted.JobID, "failed")

	resumed := resumeTestJob(api, accepted.JobID)
	if resumed.Code != http.StatusAccepted {
		t.Fatalf("resume status = %d, body: %s", resumed.Code, resumed.Body.String())
	}
	var resumedAck acknowledgement
	if err := json.Unmarshal(resumed.Body.Bytes(), &resumedAck); err != nil {
		t.Fatal(err)
	}
	if resumedAck.JobID != accepted.JobID || resumedAck.Status != "queued" {
		t.Fatalf("resume acknowledgement = %#v, want original ID queued", resumedAck)
	}
	if resumed.Header().Get("Location") != accepted.StatusURL {
		t.Fatalf("resume Location = %q, want %q", resumed.Header().Get("Location"), accepted.StatusURL)
	}
	job := waitForJobStatus(t, store, accepted.JobID, "succeeded")
	if job.Error != nil || job.ExitCode == nil || *job.ExitCode != 0 || job.ContainerLogs != "" {
		t.Fatalf("previous attempt metadata was not cleared: %#v", job)
	}
	if attempts.Load() != 2 {
		t.Fatalf("executor attempts = %d, want 2", attempts.Load())
	}
	if _, err := store.Create("input", "output", "/host/input", "/host/output"); err != nil {
		t.Fatalf("new job after resume could not be queued: %v", err)
	}
}

func TestOnlyFailedOrCancelledJobsCanBeResumed(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	api, store, cleanup := testAPI(t, testExecutor{run: func(ctx context.Context, _ Job, markRunning func(string) error, _ func([]byte) error) (int, error) {
		if err := markRunning("blocking-container"); err != nil {
			return 1, err
		}
		startedOnce.Do(func() { close(started) })
		select {
		case <-release:
			return 0, nil
		case <-ctx.Done():
			return 1, ctx.Err()
		}
	}}, time.Second)
	defer func() {
		close(release)
		cleanup()
	}()

	active := submitTestJob(api, `{"inputPath":"input","outputPath":"output","mode":"asynchronous"}`)
	if active.Code != http.StatusAccepted {
		t.Fatalf("active-job submission status = %d, body: %s", active.Code, active.Body.String())
	}
	<-started
	for _, status := range []string{"queued", "starting", "running", "cancelling", "succeeded"} {
		job, err := store.Create("input", "output", "/host/input", "/host/output")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`UPDATE jobs SET status=? WHERE id=?`, status, job.ID); err != nil {
			t.Fatal(err)
		}
		response := resumeTestJob(api, job.ID)
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"JOB_NOT_RESUMABLE"`) {
			t.Fatalf("resume %s job status = %d, body: %s", status, response.Code, response.Body.String())
		}
	}
	if response := resumeTestJob(api, "missing-job"); response.Code != http.StatusNotFound {
		t.Fatalf("resume missing job status = %d, body: %s", response.Code, response.Body.String())
	}

	cancelled, err := store.Create("input", "output", "/host/input", "/host/output")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE jobs SET status='cancelled' WHERE id=?`, cancelled.ID); err != nil {
		t.Fatal(err)
	}
	response := resumeTestJob(api, cancelled.ID)
	if response.Code != http.StatusAccepted {
		t.Fatalf("resume cancelled job status = %d, body: %s", response.Code, response.Body.String())
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

func resumeTestJob(api *API, id string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+id+"/resume", nil)
	request.Header.Set("Authorization", "Bearer "+api.config.APIKey)
	api.ServeHTTP(recorder, request)
	return recorder
}

func waitForJobStatus(t *testing.T, store *JobStore, id, status string) Job {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		job, err := store.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == status {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	job, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("job status = %q, want %q", job.Status, status)
	return Job{}
}

func multipartArchiveRequest(t *testing.T, archive []byte, withArchive bool) (*bytes.Buffer, string) {
	return multipartArchiveRequestWithMode(t, archive, withArchive, "synchronous")
}

func multipartArchiveRequestWithMode(t *testing.T, archive []byte, withArchive bool, mode string) (*bytes.Buffer, string) {
	return multipartArchiveRequestWithFields(t, archive, withArchive, mode, nil)
}

func multipartArchiveRequestWithOCR(t *testing.T, archive []byte, mode, language, optimize string) (*bytes.Buffer, string) {
	return multipartArchiveRequestWithFields(t, archive, true, mode, map[string]string{
		"language": language,
		"optimize": optimize,
	})
}

func multipartArchiveRequestWithFields(t *testing.T, archive []byte, withArchive bool, mode string, fields map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("mode", mode); err != nil {
		t.Fatal(err)
	}
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if withArchive {
		part, err := writer.CreateFormFile("inputArchive", "input.tar.gz")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(archive); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := writer.WriteField("inputPath", "input"); err != nil {
			t.Fatal(err)
		}
		if err := writer.WriteField("outputPath", "output"); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, writer.FormDataContentType()
}

func cancelTestJob(api *API, id string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+id+"/cancel", nil)
	request.Header.Set("Authorization", "Bearer "+api.config.APIKey)
	api.ServeHTTP(recorder, request)
	return recorder
}

func deleteTestJob(api *API, id string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+id, nil)
	request.Header.Set("Authorization", "Bearer "+api.config.APIKey)
	api.ServeHTTP(recorder, request)
	return recorder
}

func cleanupJobsTestRequest(api *API, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/cleanup", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+api.config.APIKey)
	request.Header.Set("Content-Type", "application/json")
	api.ServeHTTP(recorder, request)
	return recorder
}

func testArchive(t *testing.T, filename, content string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: filename, Mode: 0o640, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(tarWriter, content); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
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
