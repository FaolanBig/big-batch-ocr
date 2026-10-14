package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractTarGzipRejectsTraversalAndAbsolutePaths(t *testing.T) {
	for _, name := range []string{"../outside.txt", "nested/../../outside.txt", "/outside.txt"} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "root")
			if err := os.Mkdir(root, 0o750); err != nil {
				t.Fatal(err)
			}
			err := extractTarGzip(bytes.NewReader(testTarGzip(t, name, "unsafe")), root)
			if err == nil {
				t.Fatal("extractTarGzip accepted an unsafe archive path")
			}
			if _, err := os.Stat(filepath.Join(parent, "outside.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("archive wrote outside its extraction root: %v", err)
			}
		})
	}
}

func TestExtractTarGzipCannotFollowSymlinkOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "pivot")); err != nil {
		t.Fatal(err)
	}
	err := extractTarGzip(bytes.NewReader(testTarGzip(t, "pivot/outside.txt", "unsafe")), root)
	if err == nil {
		t.Fatal("extractTarGzip followed a symlink outside its root")
	}
	if _, err := os.Stat(filepath.Join(outside, "outside.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive wrote through an extraction-root symlink: %v", err)
	}
}

func TestExtractTarGzipWritesFilesBeneathRoot(t *testing.T) {
	root := t.TempDir()
	err := extractTarGzip(bytes.NewReader(testTarGzip(t, "nested/input.txt", "safe")), root)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "nested", "input.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "safe" {
		t.Fatalf("extracted content = %q, want safe", content)
	}
}

func testTarGzip(t *testing.T, name, content string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{
		Name: name, Mode: 0o640, Size: int64(len(content)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
