package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const RegistryURL = "https://github.com/t8y2/dbx/releases/download/agents-latest/agent-registry.json"
const maxArtifactBytes = int64(2 << 30)

var safeKey = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,79}$`)

type Artifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size"`
	Format string `json:"format,omitempty"`
}
type DriverInfo struct {
	Version string              `json:"version"`
	Label   string              `json:"label"`
	JRE     string              `json:"jre"`
	JAR     *Artifact           `json:"jar,omitempty"`
	Native  map[string]Artifact `json:"native,omitempty"`
}
type JREInfo struct {
	Version   string              `json:"version"`
	Platforms map[string]Artifact `json:"platforms"`
}
type Registry struct {
	Drivers map[string]DriverInfo `json:"drivers"`
	JRE     *JREInfo              `json:"jre,omitempty"`
	JREs    map[string]JREInfo    `json:"jres,omitempty"`
}
type JavaConfig struct {
	Mode       string  `json:"mode"`
	CustomPath *string `json:"custom_java_path"`
}
type Installed struct {
	Version     string `json:"version"`
	InstalledAt string `json:"installed_at"`
	JRE         string `json:"jre"`
}
type State struct {
	Drivers map[string]Installed `json:"installed_drivers"`
	JREs    map[string]string    `json:"jre_versions"`
	Java    JavaConfig           `json:"java_runtime"`
}
type Progress struct {
	Step        string `json:"step"`
	Downloaded  int64  `json:"downloaded"`
	Total       int64  `json:"total"`
	DBType      string `json:"db_type,omitempty"`
	OperationID string `json:"operation_id,omitempty"`
}
type Store struct {
	Root        string
	Client      *http.Client
	RegistryURL string
	mu          sync.Mutex
	operationMu contextMutex
	state       State
	registry    *Registry
	registryAt  time.Time
	active      map[*Session]string
	closed      bool
	life        context.Context
	cancel      context.CancelFunc
}

func NewStore(root string) (*Store, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	s := &Store{Root: root, RegistryURL: RegistryURL, Client: &http.Client{Timeout: 5 * time.Minute}, active: map[*Session]string{}, operationMu: make(contextMutex, 1)}
	s.life, s.cancel = context.WithCancel(context.Background())
	s.state = State{Drivers: map[string]Installed{}, JREs: map[string]string{}, Java: JavaConfig{Mode: "managed"}}
	b, err := os.ReadFile(filepath.Join(root, "state.json"))
	if err == nil {
		if err = json.Unmarshal(b, &s.state); err != nil {
			return nil, fmt.Errorf("invalid agent state: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if s.state.Drivers == nil {
		s.state.Drivers = map[string]Installed{}
	}
	if s.state.JREs == nil {
		s.state.JREs = map[string]string{}
	}
	if s.state.Java.Mode == "" {
		s.state.Java.Mode = "managed"
	}
	return s, nil
}
func Platform() string {
	platform := runtime.GOOS
	if platform == "darwin" {
		platform = "macos"
	}
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	if arch == "arm64" {
		arch = "aarch64"
	}
	return platform + "-" + arch
}
func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Root, ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(b); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(s.Root, "state.json"))
}
func (s *Store) FetchRegistry(ctx context.Context) (*Registry, error) {
	ctx, endOperation := s.operationContext(ctx)
	defer endOperation()
	s.mu.Lock()
	if s.registry != nil && time.Since(s.registryAt) < 5*time.Minute {
		r := s.registry
		s.mu.Unlock()
		return r, nil
	}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.RegistryURL, nil)
	if err != nil {
		return nil, err
	}
	res, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry HTTP %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 1<<20 {
		return nil, errors.New("agent registry exceeds 1 MiB")
	}
	var registry Registry
	if err = json.Unmarshal(b, &registry); err != nil {
		return nil, err
	}
	if registry.Drivers == nil {
		return nil, errors.New("agent registry has no drivers")
	}
	s.mu.Lock()
	s.registry = &registry
	s.registryAt = time.Now()
	s.mu.Unlock()
	return &registry, nil
}
func (s *Store) InvalidateRegistry()    { s.mu.Lock(); s.registry = nil; s.mu.Unlock() }
func (s *Store) JavaConfig() JavaConfig { s.mu.Lock(); defer s.mu.Unlock(); return s.state.Java }
func (s *Store) SetJavaConfig(ctx context.Context, config JavaConfig) (JavaConfig, error) {
	ctx, endOperation := s.operationContext(ctx)
	defer endOperation()
	if err := s.operationMu.Acquire(ctx); err != nil {
		return config, err
	}
	defer s.operationMu.Unlock()
	if len(s.ActiveKeys()) > 0 {
		return config, errors.New("close agent connections before changing Java runtime")
	}
	if config.Mode != "managed" && config.Mode != "system" && config.Mode != "custom" {
		return config, errors.New("invalid Java runtime mode")
	}
	if config.Mode != "custom" {
		config.CustomPath = nil
	}
	if config.Mode != "managed" {
		path, err := s.javaPath(config, "21")
		if err != nil {
			return config, err
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, path, "-version")
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		if err = cmd.Run(); err != nil {
			return config, fmt.Errorf("validate Java runtime: %w", err)
		}
		if config.Mode == "custom" {
			config.CustomPath = &path
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.state.Java
	s.state.Java = config
	if err := s.saveLocked(); err != nil {
		s.state.Java = old
		return config, err
	}
	return config, nil
}
func (s *Store) javaPath(config JavaConfig, key string) (string, error) {
	if config.Mode == "system" {
		return exec.LookPath("java")
	}
	if config.Mode == "custom" {
		if config.CustomPath == nil || *config.CustomPath == "" {
			return "", errors.New("custom Java path is required")
		}
		path, err := filepath.Abs(*config.CustomPath)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			return "", errors.New("custom Java executable does not exist")
		}
		return path, nil
	}
	if key == "" {
		key = "21"
	}
	if !safeKey.MatchString(key) {
		return "", errors.New("invalid JRE key")
	}
	name := "java"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	root := filepath.Join(s.Root, "jre-"+key)
	var found string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Name() == name && filepath.Base(filepath.Dir(path)) == "bin" {
			if found == "" {
				found = path
			}
		}
		return nil
	})
	if found == "" {
		return "", fmt.Errorf("managed JRE %s is not installed; install it or configure system Java", key)
	}
	return found, nil
}
func (s *Store) Launch(key string) (Launch, error) {
	if !safeKey.MatchString(key) {
		return Launch{}, errors.New("invalid agent key")
	}
	dir := filepath.Join(s.Root, "drivers", key)
	name := "agent"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	native := filepath.Join(dir, name)
	if info, err := os.Stat(native); err == nil && info.Mode().IsRegular() {
		return Launch{Command: native, Dir: dir}, nil
	}
	jar := filepath.Join(dir, "agent.jar")
	if info, err := os.Stat(jar); err != nil || !info.Mode().IsRegular() {
		return Launch{}, fmt.Errorf("agent driver %s is not installed", key)
	}
	s.mu.Lock()
	installed := s.state.Drivers[key]
	config := s.state.Java
	s.mu.Unlock()
	java, err := s.javaPath(config, installed.JRE)
	if err != nil {
		return Launch{}, err
	}
	return Launch{Command: java, Args: []string{"-Xms32m", "-Xmx512m", "-jar", jar}, Dir: dir}, nil
}
func (s *Store) Open(ctx context.Context, key string, params map[string]any) (*Session, error) {
	ctx, endOperation := s.operationContext(ctx)
	defer endOperation()
	if err := s.operationMu.Acquire(ctx); err != nil {
		return nil, err
	}
	defer s.operationMu.Unlock()
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil, errors.New("agent store is closed")
	}
	launch, err := s.Launch(key)
	if err != nil {
		return nil, err
	}
	session, err := OpenSession(ctx, launch, params)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.active[session] = key
	s.mu.Unlock()
	return session, nil
}
func (s *Store) Release(session *Session) error {
	err := session.Close()
	s.mu.Lock()
	delete(s.active, session)
	s.mu.Unlock()
	return err
}
func (s *Store) ActiveKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := map[string]bool{}
	for session, key := range s.active {
		if !session.Runtime.Failed() {
			set[key] = true
		}
	}
	keys := []string{}
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
func (s *Store) ensureInactive(key string) error {
	for _, active := range s.ActiveKeys() {
		if key == "" || active == key {
			return fmt.Errorf("close active %s database connections before changing drivers", active)
		}
	}
	return nil
}
func (s *Store) Close() error {
	s.cancel()
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	s.mu.Lock()
	s.closed = true
	sessions := s.active
	s.active = map[*Session]string{}
	s.mu.Unlock()
	var errs []error
	for session := range sessions {
		errs = append(errs, session.Close())
	}
	return errors.Join(errs...)
}
func (s *Store) IsInstalled(key string) bool {
	_, err := s.Launch(key)
	if err == nil {
		return true
	}
	if !safeKey.MatchString(key) {
		return false
	}
	info, e := os.Stat(filepath.Join(s.Root, "drivers", key, "agent.jar"))
	return e == nil && info.Mode().IsRegular()
}
func (s *Store) List(registry *Registry) []map[string]any {
	labels := Labels()
	s.mu.Lock()
	installed := map[string]Installed{}
	for k, v := range s.state.Drivers {
		installed[k] = v
	}
	java := s.state.Java
	s.mu.Unlock()
	if registry != nil {
		for k, v := range registry.Drivers {
			labels[k] = v.Label
		}
	}
	keys := []string{}
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		local := installed[key]
		version := local.Version
		jre := local.JRE
		if jre == "" {
			jre = "21"
		}
		size := dirBytes(filepath.Join(s.Root, "drivers", key))
		requiresJava := true
		if registry != nil {
			remote, ok := registry.Drivers[key]
			if ok {
				version = remote.Version
				if remote.JRE != "" {
					jre = remote.JRE
				}
				if native, ok := remote.Native[Platform()]; ok {
					size = native.Size
					requiresJava = false
				} else if remote.JAR != nil {
					size = remote.JAR.Size
				}
			}
		}
		if _, err := os.Stat(filepath.Join(s.Root, "drivers", key, "agent")); err == nil {
			requiresJava = false
		}
		_, javaErr := s.javaPath(java, jre)
		var installedVersion any
		if local.Version != "" {
			installedVersion = local.Version
		}
		result = append(result, map[string]any{"db_type": key, "label": labels[key], "version": version, "size": size, "installed": s.IsInstalled(key), "installed_version": installedVersion, "update_available": local.Version != "" && version != "" && version != local.Version, "requires_java_runtime": requiresJava, "jre": jre, "jre_installed": javaErr == nil})
	}
	return result
}
func dirBytes(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && entry.Type().IsRegular() {
			if info, e := entry.Info(); e == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}
func (s *Store) Usage() map[string]any {
	driverBytes := dirBytes(filepath.Join(s.Root, "drivers"))
	cacheBytes := dirBytes(filepath.Join(s.Root, "downloads"))
	jreBytes := int64(0)
	jres := []map[string]any{}
	drivers := []map[string]any{}
	entries, _ := os.ReadDir(s.Root)
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "jre-") {
			size := dirBytes(filepath.Join(s.Root, entry.Name()))
			jreBytes += size
			jres = append(jres, map[string]any{"id": strings.TrimPrefix(entry.Name(), "jre-"), "bytes": size})
		}
	}
	entries, _ = os.ReadDir(filepath.Join(s.Root, "drivers"))
	for _, entry := range entries {
		if entry.IsDir() {
			drivers = append(drivers, map[string]any{"id": entry.Name(), "bytes": dirBytes(filepath.Join(s.Root, "drivers", entry.Name()))})
		}
	}
	return map[string]any{"total_bytes": driverBytes + jreBytes + cacheBytes, "jre_bytes": jreBytes, "agent_driver_bytes": driverBytes, "download_cache_bytes": cacheBytes, "jdbc_plugin_bytes": 0, "jdbc_driver_bytes": 0, "jres": jres, "agent_drivers": drivers}
}
func (s *Store) ClearCache() error {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	return os.RemoveAll(filepath.Join(s.Root, "downloads"))
}
func (s *Store) Uninstall(key string) error {
	if !safeKey.MatchString(key) {
		return errors.New("invalid agent key")
	}
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	if err := s.ensureInactive(key); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(s.Root, "drivers", key)); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.state.Drivers, key)
	return s.saveLocked()
}
func checkArtifact(path string, artifact Artifact) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, maxArtifactBytes+1))
	if err != nil {
		return err
	}
	if n > maxArtifactBytes || artifact.Size > 0 && n != artifact.Size {
		return errors.New("driver artifact size mismatch")
	}
	if artifact.SHA256 != "" {
		expected, err := hex.DecodeString(artifact.SHA256)
		if err != nil || len(expected) != 32 {
			return errors.New("invalid driver checksum")
		}
		if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), artifact.SHA256) {
			return errors.New("driver artifact SHA-256 mismatch")
		}
	}
	return nil
}
func (s *Store) download(ctx context.Context, artifact Artifact, progress func(Progress), key, step string) (string, error) {
	ctx, endOperation := s.operationContext(ctx)
	defer endOperation()
	if artifact.Size < 0 || artifact.Size > maxArtifactBytes {
		return "", errors.New("invalid artifact size")
	}
	if artifact.SHA256 == "" {
		return "", errors.New("online driver artifact requires SHA-256 checksum")
	}
	if err := os.MkdirAll(filepath.Join(s.Root, "downloads"), 0700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(filepath.Join(s.Root, "downloads"), "artifact-*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	success := false
	defer func() {
		file.Close()
		if !success {
			os.Remove(path)
		}
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.URL, nil)
	if err != nil {
		return "", err
	}
	res, err := s.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("artifact HTTP %d", res.StatusCode)
	}
	reader := io.LimitReader(res.Body, maxArtifactBytes+1)
	buffer := make([]byte, 128<<10)
	var downloaded int64
	last := time.Time{}
	for {
		n, readErr := reader.Read(buffer)
		if n > 0 {
			if _, err = file.Write(buffer[:n]); err != nil {
				return "", err
			}
			downloaded += int64(n)
			if time.Since(last) > 100*time.Millisecond && progress != nil {
				progress(Progress{Step: step, DBType: key, Downloaded: downloaded, Total: artifact.Size})
				last = time.Now()
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
		if err = ctx.Err(); err != nil {
			return "", err
		}
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	if err = checkArtifact(path, artifact); err != nil {
		return "", err
	}
	success = true
	return path, nil
}
func (s *Store) Install(ctx context.Context, key string, progress func(Progress)) error {
	ctx, endOperation := s.operationContext(ctx)
	defer endOperation()
	if !safeKey.MatchString(key) {
		return errors.New("invalid agent key")
	}
	if err := s.operationMu.Acquire(ctx); err != nil {
		return err
	}
	defer s.operationMu.Unlock()
	if err := s.ensureInactive(key); err != nil {
		return err
	}
	registry, err := s.FetchRegistry(ctx)
	if err != nil {
		return err
	}
	driver, ok := registry.Drivers[key]
	if !ok {
		return fmt.Errorf("standalone driver %q is not available in the configured registry", key)
	}
	artifact, native := driver.Native[Platform()]
	if !native {
		if driver.JAR == nil {
			return fmt.Errorf("no %s agent artifact for %s", Platform(), key)
		}
		artifact = *driver.JAR
	}
	if !native {
		s.mu.Lock()
		java := s.state.Java
		s.mu.Unlock()
		if _, err = s.javaPath(java, driver.JRE); err != nil {
			if java.Mode != "managed" {
				return err
			}
			if err = s.installJRE(ctx, registry, driver.JRE, progress); err != nil {
				return err
			}
		}
	}
	path, err := s.download(ctx, artifact, progress, key, "driver")
	if err != nil {
		return err
	}
	defer os.Remove(path)
	if artifact.Format == "tar_zstd" {
		_, err = s.importPackage(ctx, path, key, driver.Version)
		return err
	}
	if artifact.Format != "" {
		return errors.New("unsupported driver artifact format")
	}
	err = s.commitDriver(ctx, key, driver, path, native)
	if err == nil && progress != nil {
		progress(Progress{Step: "done", DBType: key})
	}
	return err
}
func (s *Store) installJRE(ctx context.Context, registry *Registry, key string, progress func(Progress)) error {
	ctx, endOperation := s.operationContext(ctx)
	defer endOperation()
	if key == "" {
		key = "21"
	}
	if !safeKey.MatchString(key) {
		return errors.New("invalid JRE key")
	}
	if err := s.ensureInactive(""); err != nil {
		return err
	}
	jre, ok := registry.JREs[key]
	if !ok && key == "21" && registry.JRE != nil {
		jre = *registry.JRE
		ok = true
	}
	if !ok {
		return errors.New("JRE is not present in registry")
	}
	artifact, ok := jre.Platforms[Platform()]
	if !ok {
		return errors.New("JRE unavailable on this platform")
	}
	path, err := s.download(ctx, artifact, progress, "", "jre")
	if err != nil {
		return err
	}
	defer os.Remove(path)
	stage, err := os.MkdirTemp(s.Root, ".jre-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err = extractArchive(ctx, path, stage, artifact.Format, artifact.URL); err != nil {
		return err
	}
	if err = validateJRE(ctx, stage); err != nil {
		return err
	}
	return s.commitJRE(stage, key, jre.Version)
}
func validateJRE(ctx context.Context, stage string) error {
	name := "java"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	var java string
	_ = filepath.WalkDir(stage, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && entry.Name() == name && filepath.Base(filepath.Dir(path)) == "bin" {
			java = path
		}
		return nil
	})
	if java == "" {
		return errors.New("JRE archive contains no Java executable")
	}
	if err := validateNative(java); err != nil {
		return fmt.Errorf("invalid JRE Java executable: %w", err)
	}
	_ = os.Chmod(java, 0755)
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	cmd := exec.CommandContext(checkCtx, java, "-version")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	err := cmd.Run()
	cancel()
	if err != nil {
		return fmt.Errorf("validate downloaded JRE: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}
func (s *Store) commitJRE(stage, key, version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return replaceDirectory(stage, filepath.Join(s.Root, "jre-"+key), func() error {
		old, existed := s.state.JREs[key]
		s.state.JREs[key] = version
		if err := s.saveLocked(); err != nil {
			if existed {
				s.state.JREs[key] = old
			} else {
				delete(s.state.JREs, key)
			}
			return err
		}
		return nil
	})
}
func (s *Store) ReinstallJRE(ctx context.Context, key string, progress func(Progress)) error {
	ctx, endOperation := s.operationContext(ctx)
	defer endOperation()
	if err := s.operationMu.Acquire(ctx); err != nil {
		return err
	}
	defer s.operationMu.Unlock()
	registry, err := s.FetchRegistry(ctx)
	if err != nil {
		return err
	}
	return s.installJRE(ctx, registry, key, progress)
}
func (s *Store) UninstallJRE(key string) error {
	if key == "" {
		key = "21"
	}
	if !safeKey.MatchString(key) {
		return errors.New("invalid JRE key")
	}
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	if err := s.ensureInactive(""); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for driver, local := range s.state.Drivers {
		jre := local.JRE
		if jre == "" {
			jre = "21"
		}
		if jre == key {
			if _, err := os.Stat(filepath.Join(s.Root, "drivers", driver, "agent.jar")); err == nil {
				return fmt.Errorf("JRE %s is used by %s; uninstall the driver first", key, driver)
			}
		}
	}
	if err := os.RemoveAll(filepath.Join(s.Root, "jre-"+key)); err != nil {
		return err
	}
	delete(s.state.JREs, key)
	return s.saveLocked()
}

// Operation lifetimes are tied to the store so shutdown cancels downloads and
// lock waiters before waiting for the installation critical section.
func (s *Store) operationContext(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(s.life, cancel)
	if s.life.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}
