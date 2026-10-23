package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxAssetBytes caps a single update package download. Current packages are
// well under 200 MiB; the cap exists so a hostile or broken mirror cannot
// fill the disk.
const maxAssetBytes = int64(2 << 30)

// DownloadProgress is emitted while an update package streams in. Total is
// the asset size reported by GitHub and may be 0 when unknown.
type DownloadProgress struct {
	AttemptID  string `json:"attempt_id"`
	Version    string `json:"version"`
	Downloaded int64  `json:"downloaded"`
	Total      int64  `json:"total"`
}

type ProgressFunc func(DownloadProgress)

// streamDownload downloads rawURL into destPath, hashing while it streams.
// The file lands in a temporary sibling first and is only renamed into
// place once the checksum matches, so a partial or corrupt download can
// never be mistaken for a complete package. Progress fires at most every
// ~150ms and always once at completion.
func streamDownload(ctx context.Context, client *http.Client, rawURL, destPath, expectedSHA256 string, progress func(downloaded int64)) (string, error) {
	if expectedSHA256 == "" {
		return "", errors.New("update package requires a SHA-256 checksum")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("update download failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("update download returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > maxAssetBytes {
		return "", errors.New("update package exceeds its size limit")
	}
	temp, err := os.CreateTemp(filepath.Dir(destPath), ".download-*")
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	success := false
	defer func() {
		_ = temp.Close()
		if !success {
			_ = os.Remove(tempPath)
		}
	}()
	hash := sha256.New()
	reader := io.LimitReader(response.Body, maxAssetBytes+1)
	buffer := make([]byte, 128<<10)
	var downloaded int64
	last := time.Time{}
	for {
		n, readErr := reader.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			if _, err := temp.Write(chunk); err != nil {
				return "", err
			}
			_, _ = hash.Write(chunk)
			downloaded += int64(n)
			if progress != nil && time.Since(last) > 150*time.Millisecond {
				progress(downloaded)
				last = time.Now()
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	if downloaded > maxAssetBytes {
		return "", errors.New("update package exceeds its size limit")
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(sum, expectedSHA256) {
		return "", fmt.Errorf("update package SHA-256 mismatch (got %s)", sum)
	}
	if err := os.Rename(tempPath, destPath); err != nil {
		return "", err
	}
	success = true
	if progress != nil {
		progress(downloaded)
	}
	return sum, nil
}
