package ideinstall

import (
	"archive/zip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func extractArchive(file *os.File, destination string, maximumBytes int64) error {
	var signature [4]byte
	if _, err := file.ReadAt(signature[:], 0); err != nil {
		return errors.New("Code CLI download is not a ZIP or gzip archive")
	}
	if signature[0] == 0x1f && signature[1] == 0x8b {
		return ExtractTarGzip(file, destination, maximumBytes)
	}
	if string(signature[:]) != "PK\x03\x04" && string(signature[:]) != "PK\x05\x06" {
		return errors.New("Code CLI download is not a ZIP or gzip archive")
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	archive, err := zip.NewReader(file, info.Size())
	if err != nil {
		return err
	}
	if maximumBytes <= 0 {
		return errors.New("archive expansion limit is required")
	}
	remaining := maximumBytes
	for _, entry := range archive.File {
		name := filepath.FromSlash(entry.Name)
		if !filepath.IsLocal(name) || strings.Contains(entry.Name, "\\") {
			return errors.New("Code CLI archive contains an unsafe path")
		}
		for _, component := range strings.Split(entry.Name, "/") {
			if component == ".." {
				return errors.New("Code CLI archive contains an unsafe path")
			}
		}
		target := filepath.Join(destination, name)
		if entry.Mode().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if !entry.Mode().IsRegular() {
			return errors.New("Code CLI archive contains unsupported entries")
		}
		if entry.UncompressedSize64 > uint64(remaining) {
			return errors.New("Code CLI archive exceeds expansion limit")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := extractZIPFile(entry, target, remaining); err != nil {
			return err
		}
		remaining -= int64(entry.UncompressedSize64)
	}
	return nil
}

func extractZIPFile(entry *zip.File, target string, remaining int64) error {
	input, err := entry.Open()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	// Read through EOF to validate the ZIP checksum, without trusting its size metadata.
	written, copyErr := io.Copy(output, io.LimitReader(input, remaining+1))
	closeErr := output.Close()
	if written > remaining {
		return errors.New("Code CLI archive exceeds expansion limit")
	}
	return errors.Join(copyErr, closeErr)
}
