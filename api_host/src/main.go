package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	defaultLogger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := LoadConfig()
	if err != nil {
		defaultLogger.Error("API host stopped with an error", "error", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	if err := run(cfg, logger); err != nil {
		logger.Error("API host stopped with an error", "error", err)
		os.Exit(1)
	}
}

func healthcheck() int {
	address := os.Getenv("API_ADDRESS")
	if address == "" {
		address = ":8080"
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid API_ADDRESS")
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	if _, err := strconv.Atoi(port); err != nil {
		fmt.Fprintln(os.Stderr, "invalid API_ADDRESS port")
		return 1
	}
	client := http.Client{Timeout: 2 * time.Second}
	response, err := client.Get("http://" + net.JoinHostPort(strings.Trim(host, "[]"), port) + "/health")
	if err != nil {
		fmt.Fprintln(os.Stderr, "health endpoint unavailable")
		return 1
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "health endpoint returned non-200 status")
		return 1
	}
	return 0
}

func run(cfg Config, logger *slog.Logger) error {
	var err error
	logger.Info("selected OCR runtime automatically", "platform", cfg.RuntimePlatform)
	store, err := OpenJobStore(cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer func() {
		if err := store.Close(); err != nil {
			logger.Error("close job database", "error", err)
		}
	}()
	executor, err := NewRuntimeExecutor(cfg, logger)
	if err != nil {
		return err
	}
	defer func() {
		if err := executor.Close(); err != nil {
			logger.Error("close OCR execution client", "error", err)
		}
	}()
	interrupted, err := store.InterruptedContainerIDs()
	if err != nil {
		return err
	}
	for _, id := range interrupted {
		if err := executor.StopInterrupted(context.Background(), id); err != nil {
			logger.Warn("could not stop OCR container from interrupted job", "container_id", id, "error_type", fmt.Sprintf("%T", err))
		}
	}
	recovered, err := store.RecoverInterrupted()
	if err != nil {
		return err
	}

	jobs := NewJobService(store, executor, cfg.MaxConcurrentJobs, logger)
	for _, job := range recovered {
		jobs.notifyWebhook(job)
	}
	server := &http.Server{
		Addr:              cfg.Address,
		Handler:           NewAPI(cfg, jobs, store, logger),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	listener, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return fmt.Errorf("listen on API address: %w", err)
	}
	jobs.Start()
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("API host listening", "address", cfg.Address)
		serverErr <- server.Serve(listener)
	}()

	signals, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	select {
	case <-signals.Done():
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			jobs.Shutdown(shutdownCtx)
			return err
		}
		return nil
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Warn("HTTP server graceful shutdown failed", "error", err)
		_ = server.Close()
	}
	jobs.Shutdown(shutdownCtx)
	return nil
}
