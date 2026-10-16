package main

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestJobStoreMigratesAndPersistsOCROptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE jobs (
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
	INSERT INTO jobs(id,status,input_path,output_path,input_host,output_host,created_at)
		VALUES('legacy', 'queued', 'in', 'out', '/in', '/out', '2026-01-01T00:00:00Z');`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenJobStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	legacy, err := store.Get("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.OCRLanguage != "eng" || legacy.OCROptimize != 3 {
		t.Fatalf("legacy job defaults = %q/%d", legacy.OCRLanguage, legacy.OCROptimize)
	}
	created, err := store.Create("in", "out", "/in", "/out", OCROptions{Language: "deu+eng", Optimize: 0})
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.OCRLanguage != "deu+eng" || reloaded.OCROptimize != 0 {
		t.Fatalf("reloaded job OCR options = %q/%d", reloaded.OCRLanguage, reloaded.OCROptimize)
	}
}

func TestJobWorkersRespectConcurrencyLimit(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var active, maximum atomic.Int32
	executor := testExecutor{run: func(_ context.Context, _ Job, markRunning func(string) error, _ func([]byte) error) (int, error) {
		current := active.Add(1)
		for {
			prior := maximum.Load()
			if current <= prior || maximum.CompareAndSwap(prior, current) {
				break
			}
		}
		if err := markRunning("container"); err != nil {
			active.Add(-1)
			return 1, err
		}
		time.Sleep(20 * time.Millisecond)
		active.Add(-1)
		return 0, nil
	}}
	service := NewJobService(store, executor, 2, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.Start()
	const count = 8
	ids := make([]string, 0, count)
	for i := 0; i < count; i++ {
		job, err := service.Submit("in", "out", "/host/in", "/host/out")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, job.ID)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		done := true
		for _, id := range ids {
			job, err := store.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			if job.Status != "succeeded" {
				done = false
				break
			}
		}
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, id := range ids {
		job, err := store.Get(id)
		if err != nil || job.Status != "succeeded" {
			t.Fatalf("job %s did not complete: status=%q err=%v", id, job.Status, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	service.Shutdown(ctx)
	if got := maximum.Load(); got > 2 || got < 1 {
		t.Fatalf("maximum concurrent jobs = %d, want 1..2", got)
	}
}

func TestJobQueueRunsInSubmissionOrder(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var mu sync.Mutex
	var completed []string
	executor := testExecutor{run: func(_ context.Context, job Job, markRunning func(string) error, _ func([]byte) error) (int, error) {
		if err := markRunning("container"); err != nil {
			return 1, err
		}
		mu.Lock()
		completed = append(completed, job.ID)
		mu.Unlock()
		return 0, nil
	}}
	service := NewJobService(store, executor, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.Start()
	const count = 8
	expected := make([]string, 0, count)
	for i := 0; i < count; i++ {
		job, err := service.Submit("in", "out", "/host/in", "/host/out")
		if err != nil {
			t.Fatal(err)
		}
		expected = append(expected, job.ID)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := len(completed) == count
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	service.Shutdown(ctx)
	mu.Lock()
	defer mu.Unlock()
	if len(completed) != count {
		t.Fatalf("completed %d jobs, want %d", len(completed), count)
	}
	for i, id := range expected {
		if completed[i] != id {
			t.Fatalf("queue position %d ran %s, want %s", i, completed[i], id)
		}
	}
}

func TestJobDatabaseFileIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	store, err := OpenJobStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("database permissions = %04o, want 0600", got)
	}
}

func TestShutdownCancelsActiveWorkWithoutReportingSuccess(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	started := make(chan struct{})
	executor := testExecutor{run: func(ctx context.Context, _ Job, markRunning func(string) error, _ func([]byte) error) (int, error) {
		if err := markRunning("container"); err != nil {
			return 1, err
		}
		close(started)
		<-ctx.Done()
		return 1, ctx.Err()
	}}
	service := NewJobService(store, executor, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.Start()
	job, err := service.Submit("in", "out", "/host/in", "/host/out")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		service.Shutdown(ctx)
	}()
	wg.Wait()
	stopped, err := store.Get(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Status == "succeeded" {
		t.Fatal("interrupted OCR work was reported successful")
	}
}
