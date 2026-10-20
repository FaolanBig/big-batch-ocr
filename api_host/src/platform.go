package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

const (
	runtimeDocker     = "docker"
	runtimeKubernetes = "kubernetes"
)

type RuntimeExecutor interface {
	Executor
	Close() error
	StopInterrupted(context.Context, string) error
}

func detectRuntimePlatform() (string, error) {
	return detectRuntimePlatformFrom(os.Getenv("KUBERNETES_SERVICE_HOST"), "/var/run/secrets/kubernetes.io/serviceaccount/token")
}

func detectRuntimePlatformFrom(kubernetesHost, tokenPath string) (string, error) {
	info, err := os.Stat(tokenPath)
	if err == nil {
		if !info.IsDir() {
			return runtimeKubernetes, nil
		}
		return "", errors.New("Kubernetes service-account token path is not a regular file")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect Kubernetes service-account credentials: %w", err)
	}
	if kubernetesHost != "" {
		return "", errors.New("Kubernetes runtime detected but service-account credentials are unavailable")
	}
	return runtimeDocker, nil
}

func NewRuntimeExecutor(cfg Config, logger *slog.Logger) (RuntimeExecutor, error) {
	switch cfg.RuntimePlatform {
	case runtimeDocker:
		executor, err := NewDockerExecutor(cfg.OCRImage, cfg.OCRJobs, logger)
		if err != nil {
			return nil, err
		}
		return executor, nil
	case runtimeKubernetes:
		return NewKubernetesExecutor(cfg, logger)
	default:
		return nil, fmt.Errorf("unsupported runtime platform %q", cfg.RuntimePlatform)
	}
}

func validateRuntimeConfig(cfg *Config) error {
	platform, err := detectRuntimePlatform()
	if err != nil {
		return err
	}
	cfg.RuntimePlatform = platform
	if platform == runtimeDocker {
		if !filepath.IsAbs(cfg.DockerDataRoot) {
			return errors.New("DOCKER_DATA_ROOT must be an absolute path visible to the Docker daemon")
		}
		cfg.DockerDataRoot = filepath.Clean(cfg.DockerDataRoot)
	} else {
		cfg.DockerDataRoot = cfg.DataRoot
	}
	return nil
}

func kubernetesResourceName(jobID string) string {
	name := strings.ToLower(strings.ReplaceAll(jobID, "_", "-"))
	return "api-host-" + name
}
