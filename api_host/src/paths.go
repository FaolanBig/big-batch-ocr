package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
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
	data, err := os.OpenRoot(root)
	if err != nil {
		return ValidatedPaths{}, fmt.Errorf("%w: data root is unavailable", ErrInvalidPath)
	}
	defer data.Close()
	inputHost, err := resolveInput(data, root, input)
	if err != nil {
		return ValidatedPaths{}, err
	}
	outputHost, err := resolveOutput(data, root, output, createDestination, inputHost)
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

func resolveInput(data *os.Root, root, requestPath string) (string, error) {
	relative, err := safeRelativePath(root, requestPath)
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(root, filepath.FromSlash(relative))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("%w: input directory does not exist", ErrInvalidPath)
	}
	if !within(root, resolved) {
		return "", fmt.Errorf("%w: input directory is outside the data root", ErrInvalidPath)
	}
	resolvedRelative, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", fmt.Errorf("%w: input path could not be resolved", ErrInvalidPath)
	}
	resolvedRelative = filepath.ToSlash(resolvedRelative)
	info, err := data.Stat(resolvedRelative)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: input must be a directory", ErrInvalidPath)
	}
	if info.Mode().Perm()&0o444 == 0 || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%w: input directory is not readable", ErrInvalidPath)
	}
	dir, err := data.Open(resolvedRelative)
	if err != nil {
		return "", fmt.Errorf("%w: input directory is not readable", ErrInvalidPath)
	}
	defer dir.Close()
	if _, err := dir.Readdirnames(1); err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("%w: input directory is not readable", ErrInvalidPath)
	}
	return filepath.Clean(resolved), nil
}

func resolveOutput(data *os.Root, root, requestPath string, create bool, inputHost string) (string, error) {
	relative, err := safeRelativePath(root, requestPath)
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(root, filepath.FromSlash(relative))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err == nil {
		if !within(root, resolved) {
			return "", fmt.Errorf("%w: destination is outside the data root", ErrInvalidPath)
		}
		resolvedRelative, relErr := filepath.Rel(root, resolved)
		if relErr != nil {
			return "", fmt.Errorf("%w: destination path could not be resolved", ErrInvalidPath)
		}
		info, statErr := data.Stat(filepath.ToSlash(resolvedRelative))
		if statErr != nil {
			return "", fmt.Errorf("%w: destination could not be inspected", ErrInvalidPath)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("%w: destination must be a directory", ErrInvalidPath)
		}
		return filepath.Clean(resolved), nil
	}
	if !errors.Is(err, os.ErrNotExist) || !create {
		return "", fmt.Errorf("%w: destination does not exist", ErrInvalidPath)
	}
	existing := path.Dir(relative)
	var resolvedParent string
	for {
		existingHost := filepath.Join(root, filepath.FromSlash(existing))
		resolvedParent, err = filepath.EvalSymlinks(existingHost)
		if err == nil {
			if !within(root, resolvedParent) {
				return "", fmt.Errorf("%w: destination parent is outside the data root", ErrInvalidPath)
			}
			parentRelative, relErr := filepath.Rel(root, resolvedParent)
			if relErr != nil {
				return "", fmt.Errorf("%w: destination parent could not be resolved", ErrInvalidPath)
			}
			if _, statErr := data.Stat(filepath.ToSlash(parentRelative)); statErr != nil {
				return "", fmt.Errorf("%w: destination parent could not be inspected", ErrInvalidPath)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) || existing == "." {
			return "", fmt.Errorf("%w: destination parent is outside the data root", ErrInvalidPath)
		}
		existing = path.Dir(existing)
	}
	missingSuffix, err := filepath.Rel(filepath.FromSlash(existing), filepath.FromSlash(relative))
	if err != nil {
		return "", fmt.Errorf("%w: destination path could not be resolved", ErrInvalidPath)
	}
	prospective := filepath.Join(resolvedParent, missingSuffix)
	if prospective == inputHost || within(inputHost, prospective) || within(prospective, inputHost) {
		return "", ErrPathConflict
	}
	createRelative, err := filepath.Rel(root, prospective)
	if err != nil {
		return "", fmt.Errorf("%w: destination path could not be resolved", ErrInvalidPath)
	}
	createRelative = filepath.ToSlash(createRelative)
	if err := data.MkdirAll(createRelative, 0o750); err != nil {
		return "", fmt.Errorf("%w: destination could not be created", ErrInvalidPath)
	}
	info, err := data.Stat(createRelative)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: created destination could not be verified", ErrInvalidPath)
	}
	return filepath.Clean(prospective), nil
}

func safeRelativePath(root, requestPath string) (string, error) {
	if strings.TrimSpace(requestPath) == "" || filepath.IsAbs(requestPath) {
		return "", fmt.Errorf("%w: paths must be non-empty and relative", ErrInvalidPath)
	}
	clean := filepath.Clean(filepath.FromSlash(requestPath))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: path must stay beneath the data root", ErrInvalidPath)
	}
	relative := filepath.ToSlash(clean)
	if !fs.ValidPath(relative) {
		return "", fmt.Errorf("%w: path must stay beneath the data root", ErrInvalidPath)
	}
	candidate := filepath.Join(root, clean)
	if !within(root, candidate) {
		return "", fmt.Errorf("%w: path must stay beneath the data root", ErrInvalidPath)
	}
	return relative, nil
}

func resolveRelative(root, requestPath string) (string, error) {
	relative, err := safeRelativePath(root, requestPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, filepath.FromSlash(relative)), nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
