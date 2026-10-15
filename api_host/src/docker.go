package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
)

type DockerExecutor struct {
	client *client.Client
	image  string
	jobs   int
	logger *slog.Logger
}

func NewDockerExecutor(imageName string, ocrJobs int, logger *slog.Logger) (*DockerExecutor, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("configure Docker Engine client: %w", err)
	}
	return &DockerExecutor{client: cli, image: imageName, jobs: ocrJobs, logger: logger}, nil
}

func (e *DockerExecutor) Close() error { return e.client.Close() }

func (e *DockerExecutor) StopInterrupted(ctx context.Context, containerID string) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	timeout := 3
	stopErr := e.client.ContainerStop(timeoutCtx, containerID, container.StopOptions{Timeout: &timeout})
	removeErr := e.client.ContainerRemove(timeoutCtx, containerID, container.RemoveOptions{Force: true})
	return errors.Join(stopErr, removeErr)
}

func (e *DockerExecutor) Run(ctx context.Context, job Job, markRunning func(string) error, saveLogs func([]byte) error) (int, error) {
	_, _, err := e.client.ImageInspectWithRaw(ctx, e.image)
	if err != nil && !errdefs.IsNotFound(err) {
		return 1, fmt.Errorf("inspect configured OCR image: %w", err)
	}
	if err != nil {
		if err := e.pullImage(ctx); err != nil {
			return 1, err
		}
	}

	ocrOptions := OCROptions{Language: job.OCRLanguage, Optimize: job.OCROptimize}
	if ocrOptions.Language == "" {
		ocrOptions = defaultOCROptions(nil)
	}
	containerName := "api-host-" + job.ID
	response, err := e.client.ContainerCreate(ctx,
		&container.Config{
			Image: e.image,
			User:  "65532:65532",
			Env: []string{
				"OCR_JOBS=" + strconv.Itoa(e.jobs),
				"OCR_LANGUAGE=" + ocrOptions.Language,
				"OCR_OPTIMIZE=" + strconv.Itoa(ocrOptions.Optimize),
				"HOME=/tmp",
			},
			Labels: map[string]string{
				"com.faolanbig.big-batch-ocr.managed": "true",
				"com.faolanbig.big-batch-ocr.job-id":  job.ID,
			},
		},
		&container.HostConfig{
			Mounts: []mount.Mount{
				{Type: mount.TypeBind, Source: job.InputHost, Target: "/source", ReadOnly: true},
				{Type: mount.TypeBind, Source: job.OutputHost, Target: "/destination"},
			},
			NetworkMode: "none",
		},
		nil, nil, containerName)
	if err != nil {
		return 1, fmt.Errorf("create OCR container: %w", err)
	}
	containerID := response.ID
	cleanup := func() {
		removeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := e.client.ContainerRemove(removeCtx, containerID, container.RemoveOptions{Force: true}); err != nil {
			e.logger.Warn("could not remove completed OCR container", "job_id", job.ID, "error_type", fmt.Sprintf("%T", err))
		}
	}
	if err := e.client.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		cleanup()
		return 1, fmt.Errorf("start OCR container: %w", err)
	}
	if err := markRunning(containerID); err != nil {
		stopAndRemove := e.stopAndRemove(containerID)
		return 1, errors.Join(fmt.Errorf("persist running OCR job: %w", err), stopAndRemove)
	}
	waitCh, errCh := e.client.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)
	cancelContainer := func() error {
		logCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		e.captureLogs(logCtx, job.ID, containerID, saveLogs)
		cancel()
		if err := e.stopAndRemove(containerID); err != nil {
			e.logger.Warn("could not stop cancelled OCR container", "job_id", job.ID, "error_type", fmt.Sprintf("%T", err))
			return errors.Join(ctx.Err(), err)
		}
		return ctx.Err()
	}
	var exitCode int64
	select {
	case result, ok := <-waitCh:
		if !ok {
			e.captureLogs(ctx, job.ID, containerID, saveLogs)
			cleanup()
			return 1, errors.New("Docker returned no OCR container exit result")
		}
		exitCode = result.StatusCode
		if result.Error != nil {
			e.captureLogs(ctx, job.ID, containerID, saveLogs)
			cleanup()
			return 1, fmt.Errorf("OCR container wait failed: %s", result.Error.Message)
		}
	case err, ok := <-errCh:
		if ctx.Err() != nil {
			return 1, cancelContainer()
		}
		e.captureLogs(ctx, job.ID, containerID, saveLogs)
		cleanup()
		if !ok || err == nil {
			err = errors.New("Docker returned an empty wait error")
		}
		return 1, fmt.Errorf("wait for OCR container: %w", err)
	case <-ctx.Done():
		return 1, cancelContainer()
	}
	e.captureLogs(ctx, job.ID, containerID, saveLogs)
	cleanup()
	return int(exitCode), nil
}

func (e *DockerExecutor) pullImage(ctx context.Context) error {
	pull, err := e.client.ImagePull(ctx, e.image, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull configured OCR image: %w", err)
	}
	if _, err := io.Copy(io.Discard, pull); err != nil {
		pull.Close()
		return fmt.Errorf("read OCR image pull response: %w", err)
	}
	if err := pull.Close(); err != nil {
		return fmt.Errorf("close OCR image pull response: %w", err)
	}
	return nil
}

func (e *DockerExecutor) captureLogs(ctx context.Context, jobID, containerID string, saveLogs func([]byte) error) {
	logs, err := e.client.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true, ShowStderr: true, Timestamps: false,
	})
	if err != nil {
		e.logger.Warn("could not capture OCR container logs", "job_id", jobID, "error_type", fmt.Sprintf("%T", err))
		return
	}
	defer logs.Close()
	var tail boundedBuffer
	if _, err := io.Copy(&tail, logs); err != nil {
		e.logger.Warn("could not read OCR container logs", "job_id", jobID, "error_type", fmt.Sprintf("%T", err))
		return
	}
	if err := saveLogs(tail.Bytes()); err != nil {
		e.logger.Warn("could not persist OCR container logs", "job_id", jobID, "error_type", fmt.Sprintf("%T", err))
	}
	e.logger.Debug("captured OCR container output", "job_id", jobID, "bytes_retained", tail.Len())
}

func (e *DockerExecutor) stopAndRemove(containerID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	timeout := 3
	stopErr := e.client.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout})
	removeErr := e.client.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
	return errors.Join(stopErr, removeErr)
}

type boundedBuffer struct {
	bytes.Buffer
	max int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.max == 0 {
		b.max = 32 * 1024
	}
	original := len(p)
	if b.Len()+len(p) > b.max {
		p = p[b.Len()+len(p)-b.max:]
		b.Buffer.Reset()
	}
	_, err := b.Buffer.Write(p)
	return original, err
}
