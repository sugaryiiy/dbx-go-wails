package updater

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Safe archive extraction for update packages, following the agent
// store's rules: only regular files and directories, no absolute or
// escaping paths, no links or special files, and a total size cap.

const maxExtractBytes = int64(2 << 30)

func safeJoin(root, name string) (string, error) {
	cleaned := filepath.Clean(filepath.FromSlash(name))
	if cleaned == "." || filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("archive entry %q escapes its destination", name)
	}
	return filepath.Join(root, cleaned), nil
}

func extractZipPackage(source, destDir string) error {
	reader, err := zip.OpenReader(source)
	if err != nil {
		return fmt.Errorf("open update zip: %w", err)
	}
	defer reader.Close()
	var total int64
	for _, entry := range reader.File {
		target, err := safeJoin(destDir, entry.Name)
		if err != nil {
			return err
		}
		mode := entry.Mode()
		if mode.IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if !mode.IsRegular() {
			return fmt.Errorf("archive entry %q is not a regular file", entry.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		written, err := writeArchiveEntry(target, mode.Perm(), func() (io.ReadCloser, error) { return entry.Open() })
		if err != nil {
			return err
		}
		total += written
		if total > maxExtractBytes {
			return errors.New("update package expands beyond its size limit")
		}
	}
	return nil
}

func extractTarGzPackage(source, destDir string) error {
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("open update tarball: %w", err)
	}
	reader := tar.NewReader(gz)
	var total int64
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read update tarball: %w", err)
		}
		target, err := safeJoin(destDir, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			written, err := writeArchiveEntry(target, os.FileMode(header.Mode).Perm(), func() (io.ReadCloser, error) {
				return io.NopCloser(reader), nil
			})
			if err != nil {
				return err
			}
			total += written
			if total > maxExtractBytes {
				return errors.New("update package expands beyond its size limit")
			}
		default:
			return fmt.Errorf("archive entry %q is a link or special file", header.Name)
		}
	}
}

func writeArchiveEntry(target string, perm os.FileMode, open func() (io.ReadCloser, error)) (int64, error) {
	source, err := open()
	if err != nil {
		return 0, err
	}
	defer source.Close()
	if perm == 0 {
		perm = 0o644
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return 0, err
	}
	written, copyErr := io.Copy(file, io.LimitReader(source, maxExtractBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		return written, copyErr
	}
	return written, closeErr
}

// findExtractedFile locates a single payload file by name under an
// extraction root (top level preferred).
func findExtractedFile(root, name string) (string, error) {
	direct := filepath.Join(root, name)
	if info, err := os.Stat(direct); err == nil && !info.IsDir() {
		return direct, nil
	}
	var found string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && info.Name() == name {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("update package does not contain %s", name)
	}
	return found, nil
}

// copyFileNextToTarget copies the staged payload beside its target so the
// subsequent rename stays on one volume.
func copyFileNextToTarget(source, dest string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}
