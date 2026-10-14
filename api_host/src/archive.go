package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const maxExtractedArchiveBytes int64 = 10 << 30

func extractTarGzip(source io.Reader, destination string) error {
	gzipReader, err := gzip.NewReader(source)
	if err != nil {
		return fmt.Errorf("open gzip archive: %w", err)
	}
	defer gzipReader.Close()

	reader := tar.NewReader(gzipReader)
	var extractedBytes int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar archive: %w", err)
		}
		name, err := safeArchivePath(header.Name)
		if err != nil {
			return err
		}
		if name == "." {
			if header.Typeflag != tar.TypeDir {
				return errors.New("archive root entry must be a directory")
			}
			continue
		}
		target := filepath.Join(destination, filepath.FromSlash(name))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				return fmt.Errorf("create archive directory: %w", err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > maxExtractedArchiveBytes-extractedBytes {
				return errors.New("archive exceeds the extraction size limit")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return fmt.Errorf("create archive parent directory: %w", err)
			}
			if _, err := os.Lstat(target); err == nil {
				return errors.New("archive contains duplicate paths")
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("inspect archive destination: %w", err)
			}
			file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
			if err != nil {
				return fmt.Errorf("create archive file: %w", err)
			}
			written, copyErr := io.CopyN(file, reader, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				return fmt.Errorf("extract archive file: %w", copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close extracted archive file: %w", closeErr)
			}
			if written != header.Size {
				return io.ErrUnexpectedEOF
			}
			extractedBytes += written
		default:
			return fmt.Errorf("unsupported archive entry type %d", header.Typeflag)
		}
	}
}

func safeArchivePath(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, '\\') || strings.ContainsRune(name, '\x00') || path.IsAbs(name) {
		return "", errors.New("archive contains an invalid path")
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == ".." {
			return "", errors.New("archive path escapes the input directory")
		}
	}
	clean := path.Clean(name)
	if !fs.ValidPath(clean) && clean != "." {
		return "", errors.New("archive contains an invalid path")
	}
	return clean, nil
}

func createTarGzip(sourceDirectory, destination string) error {
	file, err := os.Create(destination)
	if err != nil {
		return fmt.Errorf("create result archive: %w", err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)

	walkErr := filepath.WalkDir(sourceDirectory, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if filePath == sourceDirectory {
			return nil
		}
		relative, err := filepath.Rel(sourceDirectory, filePath)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("result contains unsupported file type: %s", name)
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = name
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		input, err := os.Open(filePath)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tarWriter, input)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	tarErr := tarWriter.Close()
	gzipErr := gzipWriter.Close()
	fileErr := file.Close()
	return errors.Join(walkErr, tarErr, gzipErr, fileErr)
}
