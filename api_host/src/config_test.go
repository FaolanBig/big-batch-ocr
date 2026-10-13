package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigRequiresKeyUnlessAnonymous(t *testing.T) {
	root := t.TempDir()
	t.Setenv("API_ADDRESS", ":0")
	t.Setenv("DATA_ROOT", root)
	t.Setenv("DOCKER_DATA_ROOT", "/srv/data")
	t.Setenv("DATABASE_PATH", filepath.Join(t.TempDir(), "jobs.db"))
	t.Setenv("OCR_IMAGE", "ghcr.io/faolanbig/big-batch-ocr:v1.2.3")
	t.Setenv("API_KEY", "")
	t.Setenv("ALLOW_ANONYMOUS", "false")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected missing API_KEY to fail")
	}
	t.Setenv("ALLOW_ANONYMOUS", "true")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("anonymous mode should load: %v", err)
	}
	if !cfg.AllowAnonymous {
		t.Fatal("anonymous mode not enabled")
	}
}

func TestLoadConfigRejectsMutableImageAndInvalidConcurrency(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	t.Setenv("DOCKER_DATA_ROOT", "/srv/data")
	t.Setenv("DATABASE_PATH", filepath.Join(t.TempDir(), "jobs.db"))
	t.Setenv("ALLOW_ANONYMOUS", "true")
	t.Setenv("OCR_IMAGE", "example/ocr:latest")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected latest tag to fail")
	}
	t.Setenv("OCR_IMAGE", "example/ocr:v1")
	if _, err := validateOCRImage("example/ocr"); err == nil {
		t.Fatal("expected untagged image to fail")
	}
	if _, err := validateOCRImage("example/ocr@sha256:" + strings.Repeat("a", 64)); err != nil {
		t.Fatalf("digest-pinned image should be accepted: %v", err)
	}
	t.Setenv("MAX_CONCURRENT_JOBS", "0")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected zero concurrency to fail")
	}
}

func TestAbsoluteDirectoryNeedsExistingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")
	if _, err := absoluteDirectory(path, "DATA_ROOT"); err == nil {
		t.Fatal("expected non-existent directory to fail")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := absoluteDirectory(file, "DATA_ROOT"); err == nil {
		t.Fatal("expected regular file to fail")
	}
}
