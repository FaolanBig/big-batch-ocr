package main

import (
	"log/slog"
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
	t.Setenv("OCR_IMAGE", "ghcr.io/faolanbig/big-batch-ocr_ocr-sequencial:v1.2.3")
	t.Setenv("OCR_LANGUAGE", "eng")
	t.Setenv("OCR_OPTIMIZE", "3")
	t.Setenv("API_KEY", "")
	t.Setenv("ALLOW_ANONYMOUS", "false")
	t.Setenv("LOG_LEVEL", "debug")
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
	if cfg.LogLevel != slog.LevelDebug {
		t.Fatalf("log level = %v, want DEBUG", cfg.LogLevel)
	}
	if cfg.OCRLanguage != "eng" || cfg.OCROptimize != 3 {
		t.Fatalf("OCR defaults = %q/%d, want eng/3", cfg.OCRLanguage, cfg.OCROptimize)
	}
}

func TestParseLogLevel(t *testing.T) {
	for _, test := range []struct {
		value string
		want  slog.Level
	}{
		{value: "DEBUG", want: slog.LevelDebug},
		{value: "info", want: slog.LevelInfo},
		{value: "WARN", want: slog.LevelWarn},
		{value: "ERROR", want: slog.LevelError},
	} {
		got, err := parseLogLevel(test.value)
		if err != nil || got != test.want {
			t.Errorf("parseLogLevel(%q) = %v, %v; want %v", test.value, got, err, test.want)
		}
	}
	if _, err := parseLogLevel("TRACE"); err == nil {
		t.Fatal("expected unsupported log level to fail")
	}
}

func TestLoadConfigDirectoryAccessDefaultsDisabledAndCanBeEnabled(t *testing.T) {
	previous, wasSet := os.LookupEnv("ALLOW_DIRECTORY_ACCESS")
	if err := os.Unsetenv("ALLOW_DIRECTORY_ACCESS"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if wasSet {
			_ = os.Setenv("ALLOW_DIRECTORY_ACCESS", previous)
		} else {
			_ = os.Unsetenv("ALLOW_DIRECTORY_ACCESS")
		}
	})
	t.Setenv("API_KEY", "test-key")
	t.Setenv("ALLOW_ANONYMOUS", "false")
	t.Setenv("DATA_ROOT", t.TempDir())
	t.Setenv("DOCKER_DATA_ROOT", "/srv/data")
	t.Setenv("DATABASE_PATH", filepath.Join(t.TempDir(), "jobs.db"))
	t.Setenv("OCR_IMAGE", "ghcr.io/faolanbig/big-batch-ocr_ocr-sequencial:v1.2.3")
	t.Setenv("OCR_LANGUAGE", "eng")
	t.Setenv("OCR_OPTIMIZE", "3")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("load config with directory access unset: %v", err)
	}
	if cfg.AllowDirectoryAccess {
		t.Fatal("directory access should be disabled by default")
	}

	t.Setenv("ALLOW_DIRECTORY_ACCESS", "true")
	cfg, err = LoadConfig()
	if err != nil {
		t.Fatalf("load config with directory access enabled: %v", err)
	}
	if !cfg.AllowDirectoryAccess {
		t.Fatal("directory access should be enabled when explicitly configured")
	}
}

func TestLoadConfigRejectsMutableImageAndInvalidConcurrency(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	t.Setenv("DOCKER_DATA_ROOT", "/srv/data")
	t.Setenv("DATABASE_PATH", filepath.Join(t.TempDir(), "jobs.db"))
	t.Setenv("ALLOW_ANONYMOUS", "true")
	t.Setenv("OCR_IMAGE", "example/ocr:latest")
	t.Setenv("OCR_LANGUAGE", "eng")
	t.Setenv("OCR_OPTIMIZE", "3")
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
	t.Setenv("MAX_CONCURRENT_JOBS", "1")
	t.Setenv("OCR_IMAGE", "example/ocr:v1")
	t.Setenv("OCR_LANGUAGE", "eng;--force-ocr")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected invalid OCR language to fail")
	}
	t.Setenv("OCR_LANGUAGE", "deu+eng")
	t.Setenv("OCR_OPTIMIZE", "+2")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected invalid OCR optimization value to fail")
	}
}

func TestOCRConfigurationValidation(t *testing.T) {
	for _, language := range []string{"eng", "deu+eng", "chi_sim"} {
		if got, err := validateOCRLanguage(language); err != nil || got != language {
			t.Fatalf("validateOCRLanguage(%q) = %q, %v", language, got, err)
		}
	}
	for _, language := range []string{"", "+eng", "eng+", "eng++deu", "eng;--force-ocr", "eng deu"} {
		if _, err := validateOCRLanguage(language); err == nil {
			t.Errorf("validateOCRLanguage(%q) should fail", language)
		}
	}
	for _, optimize := range []int{0, 1, 2, 3} {
		if got, err := validateOCROptimize(optimize); err != nil || got != optimize {
			t.Errorf("validateOCROptimize(%d) = %d, %v", optimize, got, err)
		}
	}
	for _, optimize := range []int{-1, 4} {
		if _, err := validateOCROptimize(optimize); err == nil {
			t.Errorf("validateOCROptimize(%d) should fail", optimize)
		}
	}
	t.Setenv("OCR_OPTIMIZE", "04")
	if _, err := envOCROptimize("OCR_OPTIMIZE", 3); err == nil {
		t.Fatal("expected non-canonical optimization level to fail")
	}
}

func TestEnvOCRWorkerResources(t *testing.T) {
	t.Setenv("OCR_CPU_REQUEST", "12")
	t.Setenv("OCR_CPU_LIMIT", "16")
	t.Setenv("OCR_MEMORY_REQUEST", "2Gi")
	t.Setenv("OCR_MEMORY_LIMIT", "4Gi")
	resources, err := envOCRWorkerResources()
	if err != nil {
		t.Fatalf("parse OCR worker resources: %v", err)
	}
	if resources.Requests.Cpu().String() != "12" || resources.Requests.Memory().String() != "2Gi" ||
		resources.Limits.Cpu().String() != "16" || resources.Limits.Memory().String() != "4Gi" {
		t.Fatalf("unexpected OCR worker resources: %#v", resources)
	}

	t.Setenv("OCR_CPU_LIMIT", "")
	if _, err := envOCRWorkerResources(); err == nil {
		t.Fatal("expected incomplete OCR worker resource settings to fail")
	}
	t.Setenv("OCR_CPU_LIMIT", "8")
	if _, err := envOCRWorkerResources(); err == nil {
		t.Fatal("expected request greater than limit to fail")
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
