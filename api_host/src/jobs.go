package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type JobError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Job struct {
	ID          string     `json:"jobId"`
	Status      string     `json:"status"`
	InputPath   string     `json:"inputPath"`
	OutputPath  string     `json:"outputPath"`
	CreatedAt   time.Time  `json:"createdAt"`
	StartedAt   *time.Time `json:"startedAt"`
	CompletedAt *time.Time `json:"completedAt"`
	ExitCode    *int       `json:"exitCode"`
	Error       *JobError  `json:"error"`
	StatusURL   string     `json:"statusUrl"`

	InputHost                 string `json:"-"`
	OutputHost                string `json:"-"`
	ContainerID               string `json:"-"`
	ContainerLogs             string `json:"-"`
	OCRLanguage               string `json:"-"`
	OCROptimize               int    `json:"-"`
	WebhookURL                string `json:"-"`
	WebhookSecret             string `json:"-"`
	RootPath                  string `json:"-"`
	WebhookInsecureSkipVerify bool   `json:"-"`
}

type OCROptions struct {
	Language                  string
	Optimize                  int
	WebhookURL                string
	WebhookSecret             string
	RootPath                  string
	WebhookInsecureSkipVerify bool
}

type Executor interface {
	Run(context.Context, Job, func(string) error, func([]byte) error) (int, error)
}

type JobStore struct {
	db *sql.DB
}

var ErrJobNotResumable = errors.New("job is not resumable")

func OpenJobStore(path string) (*JobStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open job database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL;
		PRAGMA synchronous=FULL;
		CREATE TABLE IF NOT EXISTS jobs (
			queue_order INTEGER PRIMARY KEY AUTOINCREMENT,
			id TEXT NOT NULL UNIQUE,
			status TEXT NOT NULL,
			input_path TEXT NOT NULL,
			output_path TEXT NOT NULL,
			input_host TEXT NOT NULL,
			output_host TEXT NOT NULL,
			created_at TEXT NOT NULL,
			started_at TEXT,
			completed_at TEXT,
			exit_code INTEGER,
			error_code TEXT,
			error_message TEXT,
			container_id TEXT,
			container_logs TEXT,
			ocr_language TEXT NOT NULL DEFAULT 'eng',
			ocr_optimize INTEGER NOT NULL DEFAULT 3,
			webhook_url TEXT NOT NULL DEFAULT '',
			webhook_secret TEXT NOT NULL DEFAULT '',
			root_path TEXT NOT NULL DEFAULT '',
			webhook_insecure_skip_verify INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS jobs_queue_idx ON jobs(status, queue_order);`); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize job database: %w", err)
	}
	if err := ensureJobColumns(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate job settings: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("restrict job database permissions: %w", err)
	}
	return &JobStore{db: db}, nil
}

func ensureJobColumns(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(jobs)`)
	if err != nil {
		return fmt.Errorf("inspect jobs table: %w", err)
	}
	columns := make(map[string]bool)
	for rows.Next() {
		var index, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&index, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("read jobs table columns: %w", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read jobs table columns: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close jobs table columns: %w", err)
	}
	if !columns["ocr_language"] {
		if _, err := db.Exec(`ALTER TABLE jobs ADD COLUMN ocr_language TEXT NOT NULL DEFAULT 'eng'`); err != nil {
			return fmt.Errorf("add OCR language column: %w", err)
		}
	}
	if !columns["ocr_optimize"] {
		if _, err := db.Exec(`ALTER TABLE jobs ADD COLUMN ocr_optimize INTEGER NOT NULL DEFAULT 3`); err != nil {
			return fmt.Errorf("add OCR optimization column: %w", err)
		}
	}
	for _, column := range []struct {
		name string
		sql  string
	}{
		{name: "webhook_url", sql: `ALTER TABLE jobs ADD COLUMN webhook_url TEXT NOT NULL DEFAULT ''`},
		{name: "webhook_secret", sql: `ALTER TABLE jobs ADD COLUMN webhook_secret TEXT NOT NULL DEFAULT ''`},
		{name: "root_path", sql: `ALTER TABLE jobs ADD COLUMN root_path TEXT NOT NULL DEFAULT ''`},
		{name: "webhook_insecure_skip_verify", sql: `ALTER TABLE jobs ADD COLUMN webhook_insecure_skip_verify INTEGER NOT NULL DEFAULT 0`},
	} {
		if !columns[column.name] {
			if _, err := db.Exec(column.sql); err != nil {
				return fmt.Errorf("add %s column: %w", column.name, err)
			}
		}
	}
	return nil
}

func (s *JobStore) Close() error { return s.db.Close() }

func (s *JobStore) Create(input, output, inputHost, outputHost string, options ...OCROptions) (Job, error) {
	id, err := newJobID()
	if err != nil {
		return Job{}, err
	}
	return s.CreateWithID(id, input, output, inputHost, outputHost, options...)
}

func (s *JobStore) CreateWithID(id, input, output, inputHost, outputHost string, options ...OCROptions) (Job, error) {
	ocrOptions := defaultOCROptions(options)
	created := time.Now().UTC()
	_, err := s.db.Exec(`INSERT INTO jobs(id,status,input_path,output_path,input_host,output_host,created_at,
		ocr_language,ocr_optimize,webhook_url,webhook_secret,root_path,webhook_insecure_skip_verify)
		VALUES(?, 'queued', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, input, output, inputHost, outputHost, formatDBTime(created), ocrOptions.Language, ocrOptions.Optimize,
		ocrOptions.WebhookURL, ocrOptions.WebhookSecret, ocrOptions.RootPath, ocrOptions.WebhookInsecureSkipVerify)
	if err != nil {
		return Job{}, fmt.Errorf("persist queued job: %w", err)
	}
	return Job{
		ID: id, Status: "queued", InputPath: input, OutputPath: output,
		CreatedAt: created, StatusURL: statusURL(id), InputHost: inputHost, OutputHost: outputHost,
		OCRLanguage: ocrOptions.Language, OCROptimize: ocrOptions.Optimize,
		WebhookURL: ocrOptions.WebhookURL, WebhookSecret: ocrOptions.WebhookSecret, RootPath: ocrOptions.RootPath,
		WebhookInsecureSkipVerify: ocrOptions.WebhookInsecureSkipVerify,
	}, nil
}

func defaultOCROptions(options []OCROptions) OCROptions {
	if len(options) == 0 {
		return OCROptions{Language: "eng", Optimize: 3}
	}
	return options[0]
}

func (s *JobStore) Get(id string) (Job, error) {
	var job Job
	var created string
	var started, completed sql.NullString
	var exitCode sql.NullInt64
	var errorCode, errorMessage, containerID, containerLogs sql.NullString
	err := s.db.QueryRow(`SELECT id,status,input_path,output_path,input_host,output_host,created_at,
		started_at,completed_at,exit_code,error_code,error_message,container_id,container_logs,ocr_language,ocr_optimize,
		webhook_url,webhook_secret,root_path,webhook_insecure_skip_verify
		FROM jobs WHERE id=?`, id).
		Scan(&job.ID, &job.Status, &job.InputPath, &job.OutputPath, &job.InputHost, &job.OutputHost,
			&created, &started, &completed, &exitCode, &errorCode, &errorMessage, &containerID, &containerLogs,
			&job.OCRLanguage, &job.OCROptimize, &job.WebhookURL, &job.WebhookSecret, &job.RootPath,
			&job.WebhookInsecureSkipVerify)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, sql.ErrNoRows
	}
	if err != nil {
		return Job{}, fmt.Errorf("read job: %w", err)
	}
	if job.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return Job{}, fmt.Errorf("decode job creation time: %w", err)
	}
	if job.StartedAt, err = parseOptionalTime(started); err != nil {
		return Job{}, err
	}
	if job.CompletedAt, err = parseOptionalTime(completed); err != nil {
		return Job{}, err
	}
	if exitCode.Valid {
		value := int(exitCode.Int64)
		job.ExitCode = &value
	}
	if errorCode.Valid {
		job.Error = &JobError{Code: errorCode.String, Message: errorMessage.String}
	}
	if containerID.Valid {
		job.ContainerID = containerID.String
	}
	if containerLogs.Valid {
		job.ContainerLogs = containerLogs.String
	}
	job.StatusURL = statusURL(job.ID)
	return job, nil
}

func (s *JobStore) List() ([]Job, error) {
	rows, err := s.db.Query(`SELECT id FROM jobs ORDER BY queue_order`)
	if err != nil {
		return nil, fmt.Errorf("list OCR jobs: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read OCR job ID: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("read OCR jobs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close OCR job list: %w", err)
	}
	jobs := make([]Job, 0, len(ids))
	for _, id := range ids {
		job, err := s.Get(id)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (s *JobStore) ClaimNext() (Job, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRow(`SELECT id FROM jobs WHERE status='queued' ORDER BY queue_order LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, sql.ErrNoRows
	}
	if err != nil {
		return Job{}, err
	}
	started := time.Now().UTC()
	result, err := tx.Exec(`UPDATE jobs SET status='starting',started_at=? WHERE id=? AND status='queued'`,
		formatDBTime(started), id)
	if err != nil {
		return Job{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return Job{}, errors.New("could not claim queued job")
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	return s.Get(id)
}

func (s *JobStore) SetRunning(id, containerID string) error {
	result, err := s.db.Exec(`UPDATE jobs SET status='running',container_id=? WHERE id=? AND status='starting'`, containerID, id)
	return requireOneRow(result, err, "mark job running")
}

func (s *JobStore) RequestCancel(id string) (Job, bool, error) {
	now := formatDBTime(time.Now().UTC())
	result, err := s.db.Exec(`UPDATE jobs SET status='cancelled',completed_at=?,exit_code=NULL,
		error_code='JOB_CANCELLED',error_message='The job was cancelled by request.'
		WHERE id=? AND status='queued'`, now, id)
	if err != nil {
		return Job{}, false, fmt.Errorf("cancel queued job: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Job{}, false, err
	}
	if changed == 0 {
		_, err = s.db.Exec(`UPDATE jobs SET status='cancelling'
			WHERE id=? AND status IN ('starting','running')`, id)
		if err != nil {
			return Job{}, false, fmt.Errorf("request running job cancellation: %w", err)
		}
	}
	job, err := s.Get(id)
	return job, changed == 1, err
}

func (s *JobStore) Resume(id string) (Job, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Job{}, fmt.Errorf("begin job resume: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE jobs SET status='queued',queue_order=(SELECT COALESCE(MAX(queue_order),0)+1 FROM jobs),
		started_at=NULL,completed_at=NULL,exit_code=NULL,error_code=NULL,error_message=NULL,
		container_id=NULL,container_logs=NULL
		WHERE id=? AND status IN ('failed','cancelled')`, id)
	if err != nil {
		return Job{}, fmt.Errorf("requeue terminal job: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Job{}, fmt.Errorf("inspect requeued job: %w", err)
	}
	if changed == 0 {
		var exists int
		if err := tx.QueryRow(`SELECT 1 FROM jobs WHERE id=?`, id).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return Job{}, sql.ErrNoRows
			}
			return Job{}, fmt.Errorf("check job before resume: %w", err)
		}
		return Job{}, ErrJobNotResumable
	}
	if _, err := tx.Exec(`UPDATE sqlite_sequence SET seq=MAX(seq,(SELECT COALESCE(MAX(queue_order),0) FROM jobs))
		WHERE name='jobs'`); err != nil {
		return Job{}, fmt.Errorf("update job queue sequence: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Job{}, fmt.Errorf("commit job resume: %w", err)
	}
	return s.Get(id)
}

func (s *JobStore) SetLogs(id string, logs []byte) error {
	result, err := s.db.Exec(`UPDATE jobs SET container_logs=? WHERE id=?`, string(logs), id)
	return requireOneRow(result, err, "persist OCR container logs")
}

func (s *JobStore) DeleteTerminal(id string) error {
	result, err := s.db.Exec(`DELETE FROM jobs WHERE id=? AND status IN ('succeeded','failed','cancelled')`, id)
	if err != nil {
		return fmt.Errorf("delete terminal job: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect deleted terminal job: %w", err)
	}
	if changed != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *JobStore) Finish(id string, exitCode int, runErr error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin job completion: %w", err)
	}
	defer tx.Rollback()
	now := formatDBTime(time.Now().UTC())
	result, err := tx.Exec(`UPDATE jobs SET status='cancelled',completed_at=?,exit_code=NULL,
		error_code='JOB_CANCELLED',error_message='The job was cancelled by request.'
		WHERE id=? AND status='cancelling'`, now, id)
	if err != nil {
		return fmt.Errorf("persist cancelled job: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 1 {
		return tx.Commit()
	}
	if runErr != nil {
		result, err = tx.Exec(`UPDATE jobs SET status='failed',completed_at=?,exit_code=NULL,
			error_code='OCR_EXECUTION_FAILED',error_message=? WHERE id=? AND status IN ('starting','running')`,
			now, "The OCR container could not be started or monitored.", id)
	} else if exitCode == 0 {
		result, err = tx.Exec(`UPDATE jobs SET status='succeeded',completed_at=?,exit_code=0
			WHERE id=? AND status IN ('starting','running')`, now, id)
	} else {
		message := "The OCR container exited unsuccessfully."
		result, err = tx.Exec(`UPDATE jobs SET status='failed',completed_at=?,exit_code=?,error_code='OCR_EXECUTION_FAILED',
			error_message=? WHERE id=? AND status IN ('starting','running')`, now, exitCode, message, id)
	}
	if err := requireOneRow(result, err, "persist job outcome"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *JobStore) RecoverInterrupted() ([]Job, error) {
	rows, err := s.db.Query(`SELECT id FROM jobs WHERE status IN ('cancelling','starting','running')`)
	if err != nil {
		return nil, fmt.Errorf("list interrupted OCR jobs: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read interrupted OCR job ID: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("read interrupted OCR jobs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close interrupted OCR job list: %w", err)
	}

	now := formatDBTime(time.Now().UTC())
	if _, err := s.db.Exec(`UPDATE jobs SET status='cancelled',completed_at=?,exit_code=NULL,
		error_code='JOB_CANCELLED',error_message='The job was cancelled before the API host restarted.',container_id=NULL
		WHERE status='cancelling'`, now); err != nil {
		return nil, err
	}
	_, err = s.db.Exec(`UPDATE jobs SET status='failed',completed_at=?,error_code='OCR_EXECUTION_FAILED',
		error_message='The API host restarted before completion could be confirmed.',container_id=NULL
		WHERE status IN ('starting','running')`, now)
	if err != nil {
		return nil, err
	}
	recovered := make([]Job, 0, len(ids))
	for _, id := range ids {
		job, err := s.Get(id)
		if err != nil {
			return nil, err
		}
		recovered = append(recovered, job)
	}
	return recovered, nil
}

func (s *JobStore) InterruptedContainerIDs() ([]string, error) {
	rows, err := s.db.Query(`SELECT container_id FROM jobs
		WHERE status IN ('starting','running','cancelling') AND container_id IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

type JobService struct {
	store       *JobStore
	executor    Executor
	workers     int
	logger      *slog.Logger
	wake        chan struct{}
	stop        chan struct{}
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	stopOnce    sync.Once
	activeMu    sync.Mutex
	active      map[string]context.CancelFunc
	maintenance sync.RWMutex
}

func NewJobService(store *JobStore, executor Executor, workers int, logger *slog.Logger) *JobService {
	ctx, cancel := context.WithCancel(context.Background())
	return &JobService{
		store: store, executor: executor, workers: workers, logger: logger,
		wake: make(chan struct{}, 1), stop: make(chan struct{}), ctx: ctx, cancel: cancel,
		active: make(map[string]context.CancelFunc),
	}
}

func (s *JobService) Start() {
	for i := 0; i < s.workers; i++ {
		s.wg.Add(1)
		go s.worker()
	}
	s.signal()
}

func (s *JobService) Shutdown(ctx context.Context) {
	s.stopOnce.Do(func() { close(s.stop) })
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		s.cancel()
	case <-ctx.Done():
		s.cancel()
		s.wg.Wait()
	}
}

func (s *JobService) Submit(input, output, inputHost, outputHost string, options ...OCROptions) (Job, error) {
	s.maintenance.RLock()
	defer s.maintenance.RUnlock()
	job, err := s.store.Create(input, output, inputHost, outputHost, options...)
	if err != nil {
		return Job{}, err
	}
	s.signal()
	return job, nil
}

func (s *JobService) SubmitWithID(id, input, output, inputHost, outputHost string, options ...OCROptions) (Job, error) {
	s.maintenance.RLock()
	defer s.maintenance.RUnlock()
	job, err := s.store.CreateWithID(id, input, output, inputHost, outputHost, options...)
	if err != nil {
		return Job{}, err
	}
	s.signal()
	return job, nil
}

func (s *JobService) Get(id string) (Job, error) { return s.store.Get(id) }

func (s *JobService) LockMaintenance() func() {
	s.maintenance.Lock()
	return s.maintenance.Unlock
}

func (s *JobService) Cancel(id string) (Job, error) {
	job, queuedCancellation, err := s.store.RequestCancel(id)
	if err != nil {
		return Job{}, err
	}
	if queuedCancellation {
		s.notifyWebhook(job)
	}
	switch job.Status {
	case "cancelling":
		s.activeMu.Lock()
		cancel := s.active[id]
		s.activeMu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
	return job, nil
}

func (s *JobService) Resume(id string) (Job, error) {
	s.maintenance.RLock()
	defer s.maintenance.RUnlock()
	job, err := s.store.Resume(id)
	if err != nil {
		return Job{}, err
	}
	s.signal()
	return job, nil
}

func (s *JobService) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *JobService) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.stop:
			return
		default:
		}
		s.maintenance.RLock()
		job, err := s.store.ClaimNext()
		s.maintenance.RUnlock()
		if err == nil {
			s.execute(job)
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			s.logger.Error("unable to claim OCR job", "error", err)
			time.Sleep(250 * time.Millisecond)
		}
		select {
		case <-s.ctx.Done():
			return
		case <-s.stop:
			return
		case <-s.wake:
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (s *JobService) execute(job Job) {
	ctx, cancel := context.WithCancel(s.ctx)
	s.activeMu.Lock()
	s.active[job.ID] = cancel
	s.activeMu.Unlock()
	defer func() {
		s.activeMu.Lock()
		delete(s.active, job.ID)
		s.activeMu.Unlock()
		cancel()
	}()
	current, err := s.store.Get(job.ID)
	if err != nil {
		s.logger.Error("unable to read OCR job before execution", "job_id", job.ID, "error", err)
		return
	}
	if current.Status == "cancelling" {
		cancel()
	}
	s.logger.Info("OCR job execution started", "job_id", job.ID)
	exitCode, runErr := s.executor.Run(ctx, job,
		func(containerID string) error { return s.store.SetRunning(job.ID, containerID) },
		func(logs []byte) error { return s.store.SetLogs(job.ID, logs) })
	if err := s.store.Finish(job.ID, exitCode, runErr); err != nil {
		s.logger.Error("unable to persist OCR job outcome", "job_id", job.ID, "error", err)
		return
	}
	finished, err := s.store.Get(job.ID)
	if err != nil {
		s.logger.Error("unable to read final OCR job outcome", "job_id", job.ID, "error", err)
		return
	}
	s.notifyWebhook(finished)
	if finished.Status == "cancelled" {
		s.logger.Info("OCR job cancelled", "job_id", job.ID)
		return
	}
	if runErr != nil {
		s.logger.Warn("OCR job execution failed", "job_id", job.ID, "failure", "executor_error")
		return
	}
	if exitCode != 0 {
		s.logger.Warn("OCR job execution failed", "job_id", job.ID, "failure", "nonzero_exit", "exit_code", exitCode)
		return
	}
	s.logger.Info("OCR job completed", "job_id", job.ID, "status", "succeeded")
}

func (s *JobService) notifyWebhook(job Job) {
	if job.WebhookURL == "" {
		return
	}
	go func() {
		body, err := json.Marshal(struct {
			Job
			RootPath string `json:"rootPath,omitempty"`
		}{Job: job, RootPath: job.RootPath})
		if err != nil {
			s.logger.Error("could not encode OCR job webhook", "job_id", job.ID, "error", err)
			return
		}
		request, err := http.NewRequest(http.MethodPost, job.WebhookURL, bytes.NewReader(body))
		if err != nil {
			s.logger.Warn("could not create OCR job webhook request", "job_id", job.ID)
			return
		}
		request.Header.Set("Content-Type", "application/json")
		if job.WebhookSecret != "" {
			request.Header.Set("X-Webhook-Secret", job.WebhookSecret)
		}
		if job.RootPath != "" {
			request.Header.Set("X-Root-Path", job.RootPath)
		}
		client := http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		if job.WebhookInsecureSkipVerify {
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
			client.Transport = transport
		}
		response, err := client.Do(request)
		if err != nil {
			s.logger.Warn("OCR job webhook request failed", "job_id", job.ID)
			return
		}
		defer response.Body.Close()
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			s.logger.Warn("OCR job webhook returned a non-success status",
				"job_id", job.ID, "status", response.StatusCode)
		}
	}()
}

func requireOneRow(result sql.Result, err error, operation string) error {
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("%s: expected one job row to change, got %d", operation, changed)
	}
	return nil
}

func newJobID() (string, error) {
	var bytes [10]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate job id: %w", err)
	}
	return "job_" + stringsUpperHex(bytes[:]), nil
}

func stringsUpperHex(src []byte) string { return hex.EncodeToString(src) }

func statusURL(id string) string { return "/api/v1/jobs/" + id }

func formatDBTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
}

func parseOptionalTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil, fmt.Errorf("decode job time: %w", err)
	}
	return &parsed, nil
}
