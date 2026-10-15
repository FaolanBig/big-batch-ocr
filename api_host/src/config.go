package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/distribution/reference"
)

type Config struct {
	Address              string
	APIKey               string
	AllowAnonymous       bool
	AllowDirectoryAccess bool
	DataRoot             string
	DockerDataRoot       string
	DatabasePath         string
	OCRImage             string
	OCRJobs              int
	MaxConcurrentJobs    int
	SyncWaitLimit        time.Duration
}

func LoadConfig() (Config, error) {
	cfg := Config{
		Address:           envOr("API_ADDRESS", ":8080"),
		APIKey:            os.Getenv("API_KEY"),
		DataRoot:          envOr("DATA_ROOT", "/data"),
		DockerDataRoot:    envOr("DOCKER_DATA_ROOT", envOr("DATA_ROOT", "/data")),
		DatabasePath:      envOr("DATABASE_PATH", "/var/lib/api-host/jobs.db"),
		OCRImage:          os.Getenv("OCR_IMAGE"),
		OCRJobs:           3,
		MaxConcurrentJobs: 1,
		SyncWaitLimit:     30 * time.Second,
	}

	var err error
	if cfg.AllowAnonymous, err = envBool("ALLOW_ANONYMOUS", false); err != nil {
		return Config{}, err
	}
	if cfg.AllowDirectoryAccess, err = envBool("ALLOW_DIRECTORY_ACCESS", false); err != nil {
		return Config{}, err
	}
	if !cfg.AllowAnonymous && cfg.APIKey == "" {
		return Config{}, errors.New("API_KEY is required unless ALLOW_ANONYMOUS=true")
	}
	if cfg.DataRoot, err = absoluteDirectory(cfg.DataRoot, "DATA_ROOT"); err != nil {
		return Config{}, err
	}
	if !filepath.IsAbs(cfg.DockerDataRoot) {
		return Config{}, errors.New("DOCKER_DATA_ROOT must be an absolute path visible to the Docker daemon")
	}
	cfg.DockerDataRoot = filepath.Clean(cfg.DockerDataRoot)
	if !filepath.IsAbs(cfg.DatabasePath) {
		return Config{}, errors.New("DATABASE_PATH must be absolute")
	}
	cfg.DatabasePath = filepath.Clean(cfg.DatabasePath)
	if cfg.OCRImage, err = validateOCRImage(cfg.OCRImage); err != nil {
		return Config{}, err
	}

	if cfg.OCRJobs, err = envPositiveInt("OCR_JOBS", cfg.OCRJobs); err != nil {
		return Config{}, err
	}
	if cfg.MaxConcurrentJobs, err = envPositiveInt("MAX_CONCURRENT_JOBS", cfg.MaxConcurrentJobs); err != nil {
		return Config{}, err
	}
	waitSeconds, err := envPositiveInt("SYNC_WAIT_LIMIT_SECONDS", int(cfg.SyncWaitLimit.Seconds()))
	if err != nil {
		return Config{}, err
	}
	cfg.SyncWaitLimit = time.Duration(waitSeconds) * time.Second
	return cfg, nil
}

func validateOCRImage(value string) (string, error) {
	if value == "" {
		return "", errors.New("OCR_IMAGE must be set to a versioned tag or image digest")
	}
	ref, err := reference.ParseNormalizedNamed(value)
	if err != nil {
		return "", errors.New("OCR_IMAGE must be a valid image reference")
	}
	_, hasTag := ref.(reference.NamedTagged)
	_, hasDigest := ref.(reference.Digested)
	if !hasTag && !hasDigest {
		return "", errors.New("OCR_IMAGE must include an explicit version tag or digest")
	}
	if tagged, ok := ref.(reference.NamedTagged); ok && tagged.Tag() == "latest" {
		if _, pinned := ref.(reference.Digested); !pinned {
			return "", errors.New("OCR_IMAGE must not use the mutable latest tag")
		}
	}
	return value, nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) (bool, error) {
	value, exists := os.LookupEnv(key)
	if !exists {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return parsed, nil
}

func envPositiveInt(key string, fallback int) (int, error) {
	value, exists := os.LookupEnv(key)
	if !exists {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return parsed, nil
}

func absoluteDirectory(value, name string) (string, error) {
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s must be an absolute path", name)
	}
	real, err := filepath.EvalSymlinks(value)
	if err != nil {
		return "", fmt.Errorf("%s must exist and be accessible", name)
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s must be a directory", name)
	}
	return filepath.Clean(real), nil
}
