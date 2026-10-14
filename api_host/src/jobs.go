package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
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

	InputHost     string `json:"-"`
	OutputHost    string `json:"-"`
	ContainerID   string `json:"-"`
	ContainerLogs string `json:"-"`
}

type Executor interface {
	Run(context.Context, Job, func(string) error, func([]byte) error) (int, error)
}

type JobStore struct {
	db *sql.DB
}

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
			container_logs TEXT
		);
		CREATE INDEX IF NOT EXISTS jobs_queue_idx ON jobs(status, queue_order);`); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize job database: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("restrict job database permissions: %w", err)
	}
	return &JobStore{db: db}, nil
}

func (s *JobStore) Close() error { return s.db.Close() }

func (s *JobStore) Create(input, output, inputHost, outputHost string) (Job, error) {
	id, err := newJobID()
	if err != nil {
		return Job{}, err
	}
	return s.CreateWithID(id, input, output, inputHost, outputHost)
}

func (s *JobStore) CreateWithID(id, input, output, inputHost, outputHost string) (Job, error) {
	created := time.Now().UTC()
	_, err := s.db.Exec(`INSERT INTO jobs(id,status,input_path,output_path,input_host,output_host,created_at)
		VALUES(?, 'queued', ?, ?, ?, ?, ?)`,
		id, input, output, inputHost, outputHost, formatDBTime(created))
	if err != nil {
		return Job{}, fmt.Errorf("persist queued job: %w", err)
	}
	return Job{
		ID: id, Status: "queued", InputPath: input, OutputPath: output,
		CreatedAt: created, StatusURL: statusURL(id), InputHost: inputHost, OutputHost: outputHost,
	}, nil
}

func (s *JobStore) Get(id string) (Job, error) {
	var job Job
	var created string
	var started, completed sql.NullString
	var exitCode sql.NullInt64
	var errorCode, errorMessage, containerID, containerLogs sql.NullString
	err := s.db.QueryRow(`SELECT id,status,input_path,output_path,input_host,output_host,created_at,
		started_at,completed_at,exit_code,error_code,error_message,container_id,container_logs FROM jobs WHERE id=?`, id).
		Scan(&job.ID, &job.Status, &job.InputPath, &job.OutputPath, &job.InputHost, &job.OutputHost,
			&created, &started, &completed, &exitCode, &errorCode, &errorMessage, &containerID, &containerLogs)
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

func (s *JobStore) SetLogs(id string, logs []byte) error {
	result, err := s.db.Exec(`UPDATE jobs SET container_logs=? WHERE id=?`, string(logs), id)
	return requireOneRow(result, err, "persist OCR container logs")
}

func (s *JobStore) Finish(id string, exitCode int, runErr error) error {
	now := formatDBTime(time.Now().UTC())
	if runErr != nil {
		result, err := s.db.Exec(`UPDATE jobs SET status='failed',completed_at=?,exit_code=NULL,
			error_code='OCR_EXECUTION_FAILED',error_message=? WHERE id=?`,
			now, "The OCR container could not be started or monitored.", id)
		return requireOneRow(result, err, "persist failed job")
	}
	if exitCode == 0 {
		result, err := s.db.Exec(`UPDATE jobs SET status='succeeded',completed_at=?,exit_code=0 WHERE id=?`, now, id)
		return requireOneRow(result, err, "persist succeeded job")
	}
	message := "The OCR container exited unsuccessfully."
	result, err := s.db.Exec(`UPDATE jobs SET status='failed',completed_at=?,exit_code=?,error_code='OCR_EXECUTION_FAILED',
		error_message=? WHERE id=?`, now, exitCode, message, id)
	return requireOneRow(result, err, "persist failed job")
}

func (s *JobStore) RecoverInterrupted() error {
	now := formatDBTime(time.Now().UTC())
	_, err := s.db.Exec(`UPDATE jobs SET status='failed',completed_at=?,error_code='OCR_EXECUTION_FAILED',
		error_message='The API host restarted before completion could be confirmed.',container_id=NULL
		WHERE status IN ('starting','running')`, now)
	return err
}

func (s *JobStore) InterruptedContainerIDs() ([]string, error) {
	rows, err := s.db.Query(`SELECT container_id FROM jobs
		WHERE status IN ('starting','running') AND container_id IS NOT NULL`)
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
	store    *JobStore
	executor Executor
	workers  int
	logger   *slog.Logger
	wake     chan struct{}
	stop     chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	stopOnce sync.Once
}

func NewJobService(store *JobStore, executor Executor, workers int, logger *slog.Logger) *JobService {
	ctx, cancel := context.WithCancel(context.Background())
	return &JobService{
		store: store, executor: executor, workers: workers, logger: logger,
		wake: make(chan struct{}, 1), stop: make(chan struct{}), ctx: ctx, cancel: cancel,
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

func (s *JobService) Submit(input, output, inputHost, outputHost string) (Job, error) {
	job, err := s.store.Create(input, output, inputHost, outputHost)
	if err != nil {
		return Job{}, err
	}
	s.signal()
	return job, nil
}

func (s *JobService) SubmitWithID(id, input, output, inputHost, outputHost string) (Job, error) {
	job, err := s.store.CreateWithID(id, input, output, inputHost, outputHost)
	if err != nil {
		return Job{}, err
	}
	s.signal()
	return job, nil
}

func (s *JobService) Get(id string) (Job, error) { return s.store.Get(id) }

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
		job, err := s.store.ClaimNext()
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
	exitCode, runErr := s.executor.Run(s.ctx, job,
		func(containerID string) error { return s.store.SetRunning(job.ID, containerID) },
		func(logs []byte) error { return s.store.SetLogs(job.ID, logs) })
	if err := s.store.Finish(job.ID, exitCode, runErr); err != nil {
		s.logger.Error("unable to persist OCR job outcome", "job_id", job.ID, "error", err)
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
