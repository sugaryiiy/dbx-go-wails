package agent

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Only regular archive files are admitted. Links, duplicate names, absolute
// paths and traversal are rejected before they can affect the existing store.
func archivePath(root, name string) (string, error) {
	name = strings.TrimPrefix(name, "./")
	if name == "" || strings.Contains(name, "\\") || strings.Contains(name, ":") || strings.HasPrefix(name, "/") {
		return "", errors.New("unsafe archive path")
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", errors.New("archive path traversal")
		}
	}
	clean := path.Clean(name)
	if clean == "." {
		return "", errors.New("empty archive path")
	}
	return filepath.Join(root, filepath.FromSlash(clean)), nil
}
func extractArchive(ctx context.Context, source, root, format, name string) error {
	seen := map[string]bool{}
	var total int64
	entries := 0
	extract := func(name string, size int64, mode os.FileMode, reader io.Reader) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries++
		if entries > 20000 {
			return errors.New("archive has too many entries")
		}
		dest, err := archivePath(root, name)
		if err != nil {
			return err
		}
		if seen[dest] {
			return errors.New("duplicate archive entry")
		}
		seen[dest] = true
		if mode.IsDir() {
			return os.MkdirAll(dest, 0700)
		}
		if !mode.IsRegular() {
			return errors.New("archive links and special files are not supported")
		}
		if size < 0 || size > maxArtifactBytes {
			return errors.New("archive entry is too large")
		}
		total += size
		if total > 4<<30 {
			return errors.New("archive decompressed size exceeds 4 GiB")
		}
		if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return err
		}
		file, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		n, err := io.Copy(file, io.LimitReader(reader, size+1))
		if err != nil {
			return err
		}
		if n != size {
			return errors.New("archive size mismatch")
		}
		if mode.Perm()&0111 != 0 {
			if err = os.Chmod(dest, 0755); err != nil {
				return err
			}
		}
		return file.Close()
	}
	if strings.HasSuffix(strings.ToLower(name), ".zip") {
		archive, err := zip.OpenReader(source)
		if err != nil {
			return err
		}
		defer archive.Close()
		for _, entry := range archive.File {
			if entry.UncompressedSize64 > uint64(maxArtifactBytes) {
				return errors.New("archive entry too large")
			}
			reader, err := entry.Open()
			if err != nil {
				return err
			}
			err = extract(entry.Name, int64(entry.UncompressedSize64), entry.Mode(), reader)
			reader.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer file.Close()
	var reader io.Reader = file
	if format == "tar_zstd" || strings.HasSuffix(strings.ToLower(name), ".tar.zst") {
		decoder, err := zstd.NewReader(file, zstd.WithDecoderMaxMemory(256<<20))
		if err != nil {
			return err
		}
		defer decoder.Close()
		reader = decoder
	} else if strings.HasSuffix(strings.ToLower(name), ".gz") || format == "tar_gzip" {
		decoder, err := gzip.NewReader(file)
		if err != nil {
			return err
		}
		defer decoder.Close()
		reader = decoder
	} else {
		return errors.New("package must be ZIP, TAR.ZST or TAR.GZ")
	}
	archive := tar.NewReader(reader)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		mode := os.FileMode(header.Mode)
		switch header.Typeflag {
		case tar.TypeDir:
			mode |= os.ModeDir
		case tar.TypeReg, tar.TypeRegA:
		default:
			return errors.New("archive links and special files are not supported")
		}
		if err = extract(header.Name, header.Size, mode, archive); err != nil {
			return err
		}
	}
}
func replaceDirectory(stage, target string, commit func() error) error {
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	old := target + ".previous"
	if _, err := os.Stat(old); err == nil {
		return errors.New("previous driver replacement requires recovery")
	}
	exists := false
	if _, err := os.Stat(target); err == nil {
		exists = true
		if err = os.Rename(target, old); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(stage, target); err != nil {
		if exists {
			_ = os.Rename(old, target)
		}
		return err
	}
	if err := commit(); err != nil {
		removeErr := os.RemoveAll(target)
		var restoreErr error
		if exists {
			restoreErr = os.Rename(old, target)
		}
		return errors.Join(err, removeErr, restoreErr)
	}
	if exists {
		_ = os.RemoveAll(old)
	}
	return nil
}
func validateJAR(source string) error {
	archive, err := zip.OpenReader(source)
	if err != nil {
		return errors.New("invalid agent JAR")
	}
	defer archive.Close()
	for _, entry := range archive.File {
		if strings.EqualFold(entry.Name, "META-INF/MANIFEST.MF") {
			reader, err := entry.Open()
			if err != nil {
				return err
			}
			data, err := io.ReadAll(io.LimitReader(reader, 65537))
			reader.Close()
			if err != nil {
				return err
			}
			if len(data) > 65536 || !strings.Contains(string(data), "Main-Class:") {
				return errors.New("agent JAR lacks Main-Class manifest")
			}
			return nil
		}
	}
	return errors.New("agent JAR lacks manifest")
}
func validateNative(source string) error {
	// Raw imports must be native binaries, never shell scripts or arbitrary files.
	switch runtime.GOOS {
	case "darwin":
		binary, err := macho.Open(source)
		if err != nil {
			return errors.New("agent is not a Mach-O executable")
		}
		defer binary.Close()
		expected := macho.CpuAmd64
		if runtime.GOARCH == "arm64" {
			expected = macho.CpuArm64
		}
		if binary.Cpu != expected {
			return errors.New("agent binary architecture mismatch")
		}
	case "linux":
		binary, err := elf.Open(source)
		if err != nil {
			return errors.New("agent is not an ELF executable")
		}
		defer binary.Close()
		expected := elf.EM_X86_64
		if runtime.GOARCH == "arm64" {
			expected = elf.EM_AARCH64
		}
		if binary.Machine != expected {
			return errors.New("agent binary architecture mismatch")
		}
	case "windows":
		binary, err := pe.Open(source)
		if err != nil {
			return errors.New("agent is not a PE executable")
		}
		defer binary.Close()
		expected := uint16(pe.IMAGE_FILE_MACHINE_AMD64)
		if runtime.GOARCH == "arm64" {
			expected = pe.IMAGE_FILE_MACHINE_ARM64
		}
		if binary.Machine != expected {
			return errors.New("agent binary architecture mismatch")
		}
	default:
		return errors.New("unsupported agent platform")
	}
	return nil
}
func (s *Store) commitDriver(ctx context.Context, key string, driver DriverInfo, source string, native bool) error {
	ctx, endOperation := s.operationContext(ctx)
	defer endOperation()
	if !safeKey.MatchString(key) {
		return errors.New("invalid driver key")
	}
	if err := s.ensureInactive(key); err != nil {
		return err
	}
	var err error
	if native {
		err = validateNative(source)
	} else {
		err = validateJAR(source)
	}
	if err != nil {
		return err
	}
	stage, err := os.MkdirTemp(s.Root, ".driver-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	name := "agent.jar"
	mode := os.FileMode(0600)
	if native {
		name = "agent"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		mode = 0755
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(filepath.Join(stage, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if e := out.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	jre := driver.JRE
	if jre == "" {
		jre = "21"
	}
	return replaceDirectory(stage, filepath.Join(s.Root, "drivers", key), func() error {
		old, existed := s.state.Drivers[key]
		s.state.Drivers[key] = Installed{Version: driver.Version, InstalledAt: time.Now().UTC().Format(time.RFC3339), JRE: jre}
		if err := s.saveLocked(); err != nil {
			if existed {
				s.state.Drivers[key] = old
			} else {
				delete(s.state.Drivers, key)
			}
			return err
		}
		return nil
	})
}
func artifactName(artifact Artifact) string {
	u, err := url.Parse(artifact.URL)
	if err != nil {
		return ""
	}
	return path.Base(u.Path)
}
func findPackagedArtifact(root, group string, artifact Artifact) (string, error) {
	name := artifactName(artifact)
	if name == "" || name == "." || name == "/" {
		return "", errors.New("invalid packaged artifact name")
	}
	for _, candidate := range []string{filepath.Join(root, group, name), filepath.Join(root, name)} {
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() {
			if err = checkArtifact(candidate, artifact); err != nil {
				return "", err
			}
			return candidate, nil
		}
	}
	return "", fmt.Errorf("package missing %s", name)
}
func (s *Store) importPackage(ctx context.Context, source, expectedKey, expectedVersion string) (map[string]any, error) {
	ctx, endOperation := s.operationContext(ctx)
	defer endOperation()
	stage, err := os.MkdirTemp(s.Root, ".package-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	// Downloads have temporary names; zstd's magic identifies modern packages.
	name := source
	file, err := os.Open(source)
	if err != nil {
		return nil, err
	}
	magic := make([]byte, 4)
	_, _ = file.Read(magic)
	file.Close()
	format := ""
	if string(magic) == "\x28\xb5\x2f\xfd" {
		format = "tar_zstd"
		name = "package.tar.zst"
	} else if string(magic[:2]) == "PK" {
		name = "package.zip"
	}
	if err = extractArchive(ctx, source, stage, format, name); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(stage, "agent-registry.json"))
	if err != nil {
		return nil, errors.New("package lacks agent-registry.json")
	}
	if len(data) > 1<<20 {
		return nil, errors.New("package registry exceeds 1 MiB")
	}
	var registry Registry
	if err = json.Unmarshal(data, &registry); err != nil {
		return nil, err
	}
	if expectedKey != "" {
		driver, ok := registry.Drivers[expectedKey]
		if !ok || len(registry.Drivers) != 1 || expectedVersion != "" && driver.Version != expectedVersion {
			return nil, errors.New("downloaded package driver/version mismatch")
		}
	}
	count := 0
	jreCount := 0
	failures := []map[string]any{}
	// Offline JREs use the same archive validator as downloaded runtimes.
	if registry.JRE != nil {
		if registry.JREs == nil {
			registry.JREs = map[string]JREInfo{}
		}
		if _, ok := registry.JREs["21"]; !ok {
			registry.JREs["21"] = *registry.JRE
		}
	}
	for key, jre := range registry.JREs {
		artifact, ok := jre.Platforms[Platform()]
		if !ok {
			continue
		}
		err = s.ensureInactive("")
		if err == nil && !safeKey.MatchString(key) {
			err = errors.New("invalid JRE key")
		}
		var archive string
		if err == nil {
			archive, err = findPackagedArtifact(stage, "jre", artifact)
		}
		if err == nil {
			var jreStage string
			jreStage, err = os.MkdirTemp(s.Root, ".jre-*")
			if err == nil {
				err = extractArchive(ctx, archive, jreStage, artifact.Format, artifact.URL)
				if err == nil {
					err = validateJRE(ctx, jreStage)
				}
				if err == nil {
					err = s.commitJRE(jreStage, key, jre.Version)
				}
				os.RemoveAll(jreStage)
			}
		}
		if err != nil {
			failures = append(failures, map[string]any{"key": key, "is_jre": true, "error": err.Error()})
		} else {
			jreCount++
		}
	}
	for key, driver := range registry.Drivers {
		if !safeKey.MatchString(key) {
			return nil, errors.New("invalid package driver key")
		}
		artifact, native := driver.Native[Platform()]
		if !native {
			if driver.JAR == nil {
				failures = append(failures, map[string]any{"key": key, "is_jre": false, "error": "no driver for this platform"})
				continue
			}
			artifact = *driver.JAR
		}
		var path string
		path, err = findPackagedArtifact(stage, "drivers", artifact)
		if err == nil && artifact.Format != "" {
			err = errors.New("nested driver packages are unsupported")
		}
		if err == nil {
			err = s.commitDriver(ctx, key, driver, path, native)
		}
		if err != nil {
			failures = append(failures, map[string]any{"key": key, "is_jre": false, "error": err.Error()})
		} else {
			count++
		}
	}
	if expectedKey != "" && len(failures) > 0 {
		return nil, fmt.Errorf("install %s: %v", expectedKey, failures[0]["error"])
	}
	return map[string]any{"count": count, "jreCount": jreCount, "failures": failures}, nil
}
func (s *Store) ImportPackage(ctx context.Context, path string) (map[string]any, error) {
	ctx, endOperation := s.operationContext(ctx)
	defer endOperation()
	if err := s.operationMu.Acquire(ctx); err != nil {
		return nil, err
	}
	defer s.operationMu.Unlock()
	return s.importPackage(ctx, path, "", "")
}
func (s *Store) ImportDriver(ctx context.Context, key, source, name string) error {
	ctx, endOperation := s.operationContext(ctx)
	defer endOperation()
	if err := s.operationMu.Acquire(ctx); err != nil {
		return err
	}
	defer s.operationMu.Unlock()
	_, known := Labels()[key]
	if !known {
		return errors.New("unknown agent driver")
	}
	if strings.HasSuffix(strings.ToLower(name), ".zip") || strings.HasSuffix(strings.ToLower(name), ".tar.zst") {
		_, err := s.importPackage(ctx, source, key, "")
		return err
	}
	return s.commitDriver(ctx, key, DriverInfo{Version: "local", JRE: "21"}, source, !strings.HasSuffix(strings.ToLower(name), ".jar"))
}
