package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
)

type redirectDockerTransport struct {
	target string
}

func (t redirectDockerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	target, err := url.Parse(t.target)
	if err != nil {
		return nil, err
	}
	request.URL.Scheme = target.Scheme
	request.URL.Host = target.Host
	return http.DefaultTransport.RoundTrip(request)
}

func newTestDockerExecutor(t *testing.T, server *httptest.Server, logger *slog.Logger) *DockerExecutor {
	t.Helper()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := client.NewClientWithOpts(
		client.WithHost("tcp://docker.test:2375"),
		client.WithVersion("1.47"),
		client.WithHTTPClient(&http.Client{Transport: redirectDockerTransport{target: target.String()}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return &DockerExecutor{client: cli, image: "example/ocr:v1.0.0", jobs: 4, logger: logger}
}

func TestDockerExecutorUsesOnlyFixedMountsAndCapturesOutcome(t *testing.T) {
	var sawCreate, sawStart, sawWait, sawLogs, sawRemove bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caseImageInspect := r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json")
		if caseImageInspect {
			http.Error(w, `{"message":"No such image"}`, http.StatusNotFound)
			return
		}
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/images/create"):
			_, _ = io.WriteString(w, "{}")
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/create"):
			sawCreate = true
			var body struct {
				Image      string            `json:"Image"`
				Env        []string          `json:"Env"`
				Labels     map[string]string `json:"Labels"`
				HostConfig struct {
					Mounts      []mount.Mount `json:"Mounts"`
					NetworkMode string        `json:"NetworkMode"`
				} `json:"HostConfig"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode create request: %v", err)
			}
			if body.Image != "example/ocr:v1.0.0" || len(body.Env) != 1 || body.Env[0] != "OCR_JOBS=4" {
				t.Errorf("unexpected container config: %#v", body)
			}
			if len(body.HostConfig.Mounts) != 2 ||
				body.HostConfig.Mounts[0].Source != "/daemon/data/in" ||
				body.HostConfig.Mounts[0].Target != "/source" ||
				!body.HostConfig.Mounts[0].ReadOnly ||
				body.HostConfig.Mounts[1].Source != "/daemon/data/out" ||
				body.HostConfig.Mounts[1].Target != "/destination" ||
				body.HostConfig.Mounts[1].ReadOnly {
				t.Errorf("unexpected container mounts: %#v", body.HostConfig.Mounts)
			}
			if body.HostConfig.NetworkMode != "none" {
				t.Errorf("unexpected network mode: %q", body.HostConfig.NetworkMode)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"Id":"container-123","Warnings":[]}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/container-123/start"):
			sawStart = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/container-123/wait"):
			sawWait = true
			_, _ = io.WriteString(w, `{"StatusCode":0}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/container-123/logs"):
			sawLogs = true
			_, _ = io.WriteString(w, "test OCR log")
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/containers/container-123"):
			sawRemove = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Docker request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	executor := newTestDockerExecutor(t, server, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer executor.Close()
	var runningID string
	var logs string
	exitCode, err := executor.Run(context.Background(), Job{
		ID: "job_test", InputHost: "/daemon/data/in", OutputHost: "/daemon/data/out",
	}, func(id string) error {
		runningID = id
		return nil
	}, func(output []byte) error {
		logs = string(output)
		return nil
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if exitCode != 0 || runningID != "container-123" || logs != "test OCR log" {
		t.Fatalf("unexpected result: exit=%d container=%q logs=%q", exitCode, runningID, logs)
	}
	if !sawCreate || !sawStart || !sawWait || !sawLogs || !sawRemove {
		t.Fatalf("Docker lifecycle incomplete: create=%v start=%v wait=%v logs=%v remove=%v",
			sawCreate, sawStart, sawWait, sawLogs, sawRemove)
	}
}

func TestDockerExecutorReturnsImagePullFailure(t *testing.T) {
	createCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json") {
			http.Error(w, `{"message":"No such image"}`, http.StatusNotFound)
			return
		}
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/images/create") {
			http.Error(w, `{"message":"daemon unavailable"}`, http.StatusInternalServerError)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/containers/create") {
			createCalled = true
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	executor := newTestDockerExecutor(t, server, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer executor.Close()
	_, err := executor.Run(context.Background(), Job{ID: "job_test"}, func(string) error { return nil }, func([]byte) error { return nil })
	if err == nil {
		t.Fatal("expected Docker image pull failure")
	}
	if createCalled {
		t.Fatal("container creation proceeded after image pull failure")
	}
}
