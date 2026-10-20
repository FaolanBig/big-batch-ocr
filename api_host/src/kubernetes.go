package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	kubernetesNamespacePath = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
)

type KubernetesExecutor struct {
	client           kubernetes.Interface
	namespace        string
	serviceAccount   string
	dataClaim        string
	dataClaimSubPath string
	dataRoot         string
	image            string
	jobs             int
	logger           *slog.Logger
}

func NewKubernetesExecutor(cfg Config, logger *slog.Logger) (*KubernetesExecutor, error) {
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("configure Kubernetes client from in-cluster credentials: %w", err)
	}
	restConfig.Timeout = 30 * time.Second
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	namespaceBytes, err := os.ReadFile(kubernetesNamespacePath)
	if err != nil {
		return nil, fmt.Errorf("read Kubernetes namespace: %w", err)
	}
	namespace := strings.TrimSpace(string(namespaceBytes))
	podName, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("resolve API host pod name: %w", err)
	}
	pod, err := clientset.CoreV1().Pods(namespace).Get(context.Background(), podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("inspect API host pod %q: %w", podName, err)
	}
	claimName, claimSubPath, serviceAccount, err := findDataClaim(pod, cfg.DataRoot)
	if err != nil {
		return nil, err
	}
	return &KubernetesExecutor{
		client: clientset, namespace: namespace, serviceAccount: serviceAccount,
		dataClaim: claimName, dataClaimSubPath: claimSubPath, dataRoot: cfg.DataRoot,
		image: cfg.OCRImage, jobs: cfg.OCRJobs, logger: logger,
	}, nil
}

func findDataClaim(pod *corev1.Pod, dataRoot string) (string, string, string, error) {
	var volumeName, subPath string
	for _, container := range pod.Spec.Containers {
		for _, volumeMount := range container.VolumeMounts {
			if filepath.Clean(volumeMount.MountPath) == filepath.Clean(dataRoot) {
				if volumeMount.SubPathExpr != "" {
					return "", "", "", errors.New("Kubernetes DATA_ROOT volume must not use subPathExpr")
				}
				volumeName, subPath = volumeMount.Name, volumeMount.SubPath
				break
			}
		}
		if volumeName != "" {
			break
		}
	}
	if volumeName == "" {
		return "", "", "", fmt.Errorf("Kubernetes API host pod must mount its data volume at DATA_ROOT (%s)", dataRoot)
	}
	for _, volume := range pod.Spec.Volumes {
		if volume.Name != volumeName {
			continue
		}
		if volume.PersistentVolumeClaim == nil || volume.PersistentVolumeClaim.ClaimName == "" {
			return "", "", "", errors.New("Kubernetes DATA_ROOT must be backed by a PersistentVolumeClaim shared with OCR jobs")
		}
		return volume.PersistentVolumeClaim.ClaimName, subPath, pod.Spec.ServiceAccountName, nil
	}
	return "", "", "", fmt.Errorf("Kubernetes DATA_ROOT volume %q is missing from API host pod", volumeName)
}

func (e *KubernetesExecutor) Close() error { return nil }

func (e *KubernetesExecutor) StopInterrupted(ctx context.Context, jobName string) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	propagation := metav1.DeletePropagationBackground
	err := e.client.BatchV1().Jobs(e.namespace).Delete(timeoutCtx, jobName, metav1.DeleteOptions{PropagationPolicy: &propagation})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete interrupted Kubernetes OCR job: %w", err)
	}
	return nil
}

func (e *KubernetesExecutor) Run(ctx context.Context, job Job, markRunning func(string) error, saveLogs func([]byte) error) (int, error) {
	name := kubernetesResourceName(job.ID)
	inputSubPath, err := e.dataSubPath(job.InputHost)
	if err != nil {
		return 1, err
	}
	outputSubPath, err := e.dataSubPath(job.OutputHost)
	if err != nil {
		return 1, err
	}
	backoffLimit := int32(0)
	automountToken := false
	worker := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "big-batch-ocr",
				"app.kubernetes.io/component": "ocr-job",
				"big-batch-ocr/job-id":        job.ID,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"big-batch-ocr/job-id": job.ID}},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					ServiceAccountName:           e.serviceAccount,
					AutomountServiceAccountToken: &automountToken,
					SecurityContext:              &corev1.PodSecurityContext{RunAsNonRoot: boolPointer(true), RunAsUser: int64Pointer(65532), RunAsGroup: int64Pointer(65532)},
					Containers: []corev1.Container{{
						Name:            "ocr",
						Image:           e.image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Env: []corev1.EnvVar{
							{Name: "OCR_JOBS", Value: fmt.Sprintf("%d", e.jobs)},
							{Name: "OCR_LANGUAGE", Value: job.OCRLanguage},
							{Name: "OCR_OPTIMIZE", Value: fmt.Sprintf("%d", job.OCROptimize)},
							{Name: "HOME", Value: "/tmp"},
						},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: boolPointer(false),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "data", MountPath: "/source", SubPath: inputSubPath, ReadOnly: true},
							{Name: "data", MountPath: "/destination", SubPath: outputSubPath},
						},
					}},
					Volumes: []corev1.Volume{{
						Name: "data",
						VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: e.dataClaim,
						}},
					}},
				},
			},
		},
	}
	if err := markRunning(name); err != nil {
		return 1, fmt.Errorf("persist running OCR job: %w", err)
	}
	_, err = e.client.BatchV1().Jobs(e.namespace).Create(ctx, worker, metav1.CreateOptions{})
	if err != nil {
		return 1, fmt.Errorf("create Kubernetes OCR job: %w", err)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		state, err := e.client.BatchV1().Jobs(e.namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if ctx.Err() != nil {
				return 1, e.cancelJob(name, ctx.Err())
			}
			if cleanupErr := e.StopInterrupted(context.Background(), name); cleanupErr != nil {
				err = errors.Join(err, cleanupErr)
			}
			return 1, fmt.Errorf("monitor Kubernetes OCR job: %w", err)
		}
		if state.Status.Succeeded > 0 || state.Status.Failed > 0 {
			e.captureLogs(ctx, name, saveLogs)
			exitCode := 0
			if state.Status.Failed > 0 {
				exitCode = 1
			}
			if err := e.StopInterrupted(context.Background(), name); err != nil {
				e.logger.Warn("could not remove completed Kubernetes OCR job", "job_id", job.ID, "error", err)
			}
			return exitCode, nil
		}
		select {
		case <-ctx.Done():
			return 1, e.cancelJob(name, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (e *KubernetesExecutor) dataSubPath(hostPath string) (string, error) {
	relative, err := filepath.Rel(e.dataRoot, hostPath)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) || stringsHasParent(relative) {
		return "", fmt.Errorf("Kubernetes OCR path %q is outside DATA_ROOT", hostPath)
	}
	subPath := filepath.ToSlash(relative)
	if e.dataClaimSubPath != "" {
		subPath = path.Join(e.dataClaimSubPath, subPath)
	}
	return subPath, nil
}

func (e *KubernetesExecutor) captureLogs(ctx context.Context, jobName string, saveLogs func([]byte) error) {
	pods, err := e.client.CoreV1().Pods(e.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "batch.kubernetes.io/job-name=" + jobName,
	})
	if err != nil {
		e.logger.Warn("could not locate Kubernetes OCR pod logs", "job_name", jobName, "error", err)
		return
	}
	if len(pods.Items) == 0 {
		e.logger.Warn("Kubernetes OCR pod was unavailable for log capture", "job_name", jobName)
		return
	}
	stream, err := e.client.CoreV1().Pods(e.namespace).GetLogs(pods.Items[0].Name, &corev1.PodLogOptions{Container: "ocr"}).Stream(ctx)
	if err != nil {
		e.logger.Warn("could not read Kubernetes OCR pod logs", "job_name", jobName, "error", err)
		return
	}
	defer stream.Close()
	var tail boundedBuffer
	if _, err := io.Copy(&tail, stream); err != nil {
		e.logger.Warn("could not read Kubernetes OCR pod logs", "job_name", jobName, "error", err)
		return
	}
	if err := saveLogs(tail.Bytes()); err != nil {
		e.logger.Warn("could not persist Kubernetes OCR logs", "job_name", jobName, "error", err)
	}
}

func (e *KubernetesExecutor) cancelJob(name string, cause error) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.StopInterrupted(cleanupCtx, name); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func stringsHasParent(value string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(value), "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

func boolPointer(value bool) *bool    { return &value }
func int64Pointer(value int64) *int64 { return &value }
