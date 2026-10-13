package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidatePathsAndCreateDestination(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "in")
	if err := os.Mkdir(input, 0o750); err != nil {
		t.Fatal(err)
	}
	paths, err := ValidatePaths(root, "/daemon/data", "in", "nested/out", true)
	if err != nil {
		t.Fatalf("ValidatePaths() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "nested", "out")); err != nil {
		t.Fatalf("destination not created: %v", err)
	}
	if paths.InputHost != "/daemon/data/in" || paths.OutputHost != "/daemon/data/nested/out" {
		t.Fatalf("unexpected daemon paths: %#v", paths)
	}
	if _, err := ValidatePaths(root, "/daemon/data", "in", "missing", false); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("missing destination error = %v", err)
	}
}

func TestValidatePathsRejectsTraversalAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "in"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"../outside", "escape"} {
		if _, err := ValidatePaths(root, "/daemon/data", input, "dest", false); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("input %q error = %v, want ErrInvalidPath", input, err)
		}
	}
}

func TestValidatePathsRejectsSameAndNestedDirectories(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "in", "out")
	if err := os.MkdirAll(output, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, outputPath := range []string{"in", "in/out"} {
		if _, err := ValidatePaths(root, "/daemon/data", "in", outputPath, false); !errors.Is(err, ErrPathConflict) {
			t.Errorf("output %q error = %v, want ErrPathConflict", outputPath, err)
		}
	}
	missingNested := filepath.Join(root, "in", "new-output")
	if _, err := ValidatePaths(root, "/daemon/data", "in", "in/new-output", true); !errors.Is(err, ErrPathConflict) {
		t.Fatalf("new nested output error = %v, want ErrPathConflict", err)
	}
	if _, err := os.Stat(missingNested); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected nested output should not be created, stat error = %v", err)
	}
}

func TestValidatePathsPreservesSymlinkThatStaysInsideRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "real-in"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "out"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real-in"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePaths(root, "/daemon/data", "alias", "out", false); err != nil {
		t.Fatalf("internal symlink should be valid: %v", err)
	}
}

func TestValidatePathsRejectsUnreadableInput(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "in")
	if err := os.Mkdir(input, 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePaths(root, "/daemon/data", "in", ".", false); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("unreadable input error = %v, want ErrInvalidPath", err)
	}
}
