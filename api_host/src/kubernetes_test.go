package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestFindDataClaimRequiresPVCMountedAtDataRoot(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			ServiceAccountName: "api-host",
			Containers: []corev1.Container{{VolumeMounts: []corev1.VolumeMount{{
				Name: "shared-data", MountPath: "/data", SubPath: "api-data",
			}}}},
			Volumes: []corev1.Volume{{Name: "shared-data", VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "ocr-data"},
			}}},
		},
	}
	claim, subPath, serviceAccount, err := findDataClaim(pod, "/data")
	if err != nil {
		t.Fatal(err)
	}
	if claim != "ocr-data" || subPath != "api-data" || serviceAccount != "api-host" {
		t.Fatalf("discovered claim = %q/%q/%q", claim, subPath, serviceAccount)
	}
	pod.Spec.Volumes[0].PersistentVolumeClaim = nil
	if _, _, _, err := findDataClaim(pod, "/data"); err == nil {
		t.Fatal("expected non-PVC data root to be rejected")
	}
}

func TestKubernetesExecutorCreatesJobWithSharedDataSubpaths(t *testing.T) {
	client := fake.NewSimpleClientset()
	var submitted *batchv1.Job
	client.PrependReactor("create", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		submitted = action.(k8stesting.CreateAction).GetObject().(*batchv1.Job).DeepCopy()
		return false, nil, nil
	})
	client.PrependReactor("get", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, &batchv1.Job{Status: batchv1.JobStatus{Succeeded: 1}}, nil
	})
	executor := &KubernetesExecutor{
		client: client, namespace: "default", serviceAccount: "api-host",
		imagePullSecrets: []corev1.LocalObjectReference{{Name: "registry-credentials"}},
		dataClaim:        "ocr-data", dataClaimSubPath: "api-data", dataRoot: "/data",
		image: "example/ocr:v1.0.0", jobs: 4,
		workerResources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("12"),
				corev1.ResourceMemory: resource.MustParse("2Gi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("16"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
			},
		},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	runningName := ""
	exitCode, err := executor.Run(context.Background(), Job{
		ID: "job_123", InputHost: "/data/invoices", OutputHost: "/data/processed",
		OCRLanguage: "deu+eng", OCROptimize: 2,
	}, func(name string) error {
		runningName = name
		return nil
	}, func([]byte) error { return nil })
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if exitCode != 0 || runningName != "api-host-job-123" {
		t.Fatalf("Run() = exit %d, running job %q", exitCode, runningName)
	}
	if submitted == nil {
		t.Fatal("Kubernetes OCR Job was not submitted")
	}
	spec := submitted.Spec.Template.Spec
	if spec.Containers[0].Image != "example/ocr:v1.0.0" || spec.Containers[0].Env[0].Value != "4" ||
		spec.Containers[0].Env[1].Value != "deu+eng" || spec.Containers[0].Env[2].Value != "2" {
		t.Fatalf("unexpected OCR container image/settings: %#v", spec.Containers[0])
	}
	if spec.Containers[0].Resources.Requests.Cpu().String() != "12" ||
		spec.Containers[0].Resources.Requests.Memory().String() != "2Gi" ||
		spec.Containers[0].Resources.Limits.Cpu().String() != "16" ||
		spec.Containers[0].Resources.Limits.Memory().String() != "4Gi" {
		t.Fatalf("unexpected OCR container resources: %#v", spec.Containers[0].Resources)
	}
	mounts := spec.Containers[0].VolumeMounts
	if len(mounts) != 2 || mounts[0].MountPath != "/source" || mounts[0].SubPath != "api-data/invoices" ||
		!mounts[0].ReadOnly || mounts[1].MountPath != "/destination" || mounts[1].SubPath != "api-data/processed" {
		t.Fatalf("unexpected OCR mounts: %#v", mounts)
	}
	if spec.Volumes[0].PersistentVolumeClaim.ClaimName != "ocr-data" ||
		spec.ServiceAccountName != "api-host" || spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken {
		t.Fatalf("unexpected OCR pod settings: %#v", spec)
	}
	if len(spec.ImagePullSecrets) != 1 || spec.ImagePullSecrets[0].Name != "registry-credentials" {
		t.Fatalf("OCR image pull secrets = %#v", spec.ImagePullSecrets)
	}
}

func TestKubernetesExecutorCleansUpJobAfterCreateError(t *testing.T) {
	client := fake.NewSimpleClientset()
	deleted := false
	client.PrependReactor("create", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("request timed out")
	})
	client.PrependReactor("delete", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		deleted = true
		return true, nil, nil
	})
	executor := &KubernetesExecutor{
		client: client, namespace: "default", dataRoot: "/data",
		dataClaim: "ocr-data", image: "example/ocr:v1.0.0",
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	_, err := executor.Run(context.Background(), Job{
		ID: "job_456", InputHost: "/data/in", OutputHost: "/data/out",
	}, func(string) error { return nil }, func([]byte) error { return nil })
	if err == nil {
		t.Fatal("expected Kubernetes Job creation error")
	}
	if !deleted {
		t.Fatal("expected cleanup after uncertain Kubernetes Job creation")
	}
}

func TestDetectRuntimePlatformUsesKubernetesCredentialsWithoutPlatformFlag(t *testing.T) {
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("service-account"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := detectRuntimePlatformFrom("10.0.0.1", token); err != nil || got != runtimeKubernetes {
		t.Fatalf("Kubernetes detection = %q, %v", got, err)
	}
	if got, err := detectRuntimePlatformFrom("", filepath.Join(t.TempDir(), "missing-token")); err != nil || got != runtimeDocker {
		t.Fatalf("Docker detection = %q, %v", got, err)
	}
	if _, err := detectRuntimePlatformFrom("10.0.0.1", filepath.Join(t.TempDir(), "missing-token")); err == nil {
		t.Fatal("expected missing Kubernetes credentials to fail rather than select Docker")
	}
}
