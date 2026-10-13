package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrInvalidPath  = errors.New("invalid path")
	ErrPathConflict = errors.New("incompatible paths")
)

type ValidatedPaths struct {
	InputPath  string
	OutputPath string
	InputHost  string
	OutputHost string
}

func ValidatePaths(dataRoot, dockerDataRoot, input, output string, createDestination bool) (ValidatedPaths, error) {
	root, err := filepath.EvalSymlinks(dataRoot)
	if err != nil {
		return ValidatedPaths{}, fmt.Errorf("%w: data root is unavailable", ErrInvalidPath)
	}
	inputHost, err := resolveInput(root, input)
	if err != nil {
		return ValidatedPaths{}, err
	}
	outputHost, err := resolveOutput(root, output, createDestination, inputHost)
	if err != nil {
		return ValidatedPaths{}, err
	}
	if inputHost == outputHost || within(inputHost, outputHost) || within(outputHost, inputHost) {
		return ValidatedPaths{}, ErrPathConflict
	}
	relInput, err := filepath.Rel(root, inputHost)
	if err != nil {
		return ValidatedPaths{}, fmt.Errorf("%w: cannot resolve input path", ErrInvalidPath)
	}
	relOutput, err := filepath.Rel(root, outputHost)
	if err != nil {
		return ValidatedPaths{}, fmt.Errorf("%w: cannot resolve output path", ErrInvalidPath)
	}
	return ValidatedPaths{
		InputPath:  filepath.ToSlash(filepath.Clean(input)),
		OutputPath: filepath.ToSlash(filepath.Clean(output)),
		InputHost:  filepath.Join(dockerDataRoot, relInput),
		OutputHost: filepath.Join(dockerDataRoot, relOutput),
	}, nil
}

func resolveInput(root, requestPath string) (string, error) {
	candidate, err := resolveRelative(root, requestPath)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("%w: input directory does not exist", ErrInvalidPath)
	}
	if !within(root, resolved) {
		return "", fmt.Errorf("%w: input directory is outside the data root", ErrInvalidPath)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: input must be a directory", ErrInvalidPath)
	}
	if info.Mode().Perm()&0o444 == 0 || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%w: input directory is not readable", ErrInvalidPath)
	}
	dir, err := os.Open(resolved)
	if err != nil {
		return "", fmt.Errorf("%w: input directory is not readable", ErrInvalidPath)
	}
	defer dir.Close()
	if _, err := dir.Readdirnames(1); err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("%w: input directory is not readable", ErrInvalidPath)
	}
	return filepath.Clean(resolved), nil
}

func resolveOutput(root, requestPath string, create bool, inputHost string) (string, error) {
	candidate, err := resolveRelative(root, requestPath)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err == nil {
		if !within(root, resolved) {
			return "", fmt.Errorf("%w: destination is outside the data root", ErrInvalidPath)
		}
		info, statErr := os.Stat(resolved)
		if statErr != nil || !info.IsDir() {
			return "", fmt.Errorf("%w: destination must be a directory", ErrInvalidPath)
		}
		return filepath.Clean(resolved), nil
	}
	if !errors.Is(err, os.ErrNotExist) || !create {
		return "", fmt.Errorf("%w: destination does not exist", ErrInvalidPath)
	}
	existing := filepath.Dir(candidate)
	var resolvedParent string
	var parentErr error
	for {
		resolvedParent, parentErr = filepath.EvalSymlinks(existing)
		if parentErr == nil {
			if !within(root, resolvedParent) {
				return "", fmt.Errorf("%w: destination parent is outside the data root", ErrInvalidPath)
			}
			break
		}
		if !errors.Is(parentErr, os.ErrNotExist) || existing == root {
			return "", fmt.Errorf("%w: destination parent is outside the data root", ErrInvalidPath)
		}
		next := filepath.Dir(existing)
		if next == existing {
			return "", fmt.Errorf("%w: destination parent is outside the data root", ErrInvalidPath)
		}
		existing = next
	}
	missingSuffix, err := filepath.Rel(existing, candidate)
	if err != nil {
		return "", fmt.Errorf("%w: destination path could not be resolved", ErrInvalidPath)
	}
	prospective := filepath.Join(resolvedParent, missingSuffix)
	if prospective == inputHost || within(inputHost, prospective) || within(prospective, inputHost) {
		return "", ErrPathConflict
	}
	if err := os.MkdirAll(candidate, 0o750); err != nil {
		return "", fmt.Errorf("%w: destination could not be created", ErrInvalidPath)
	}
	resolved, err = filepath.EvalSymlinks(candidate)
	if err != nil || !within(root, resolved) {
		return "", fmt.Errorf("%w: created destination is outside the data root", ErrInvalidPath)
	}
	return filepath.Clean(resolved), nil
}

func resolveRelative(root, requestPath string) (string, error) {
	if strings.TrimSpace(requestPath) == "" || filepath.IsAbs(requestPath) {
		return "", fmt.Errorf("%w: paths must be non-empty and relative", ErrInvalidPath)
	}
	clean := filepath.Clean(filepath.FromSlash(requestPath))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: path must stay beneath the data root", ErrInvalidPath)
	}
	candidate := filepath.Join(root, clean)
	if !within(root, candidate) {
		return "", fmt.Errorf("%w: path must stay beneath the data root", ErrInvalidPath)
	}
	return candidate, nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
