package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const storeHeader = "DBX-GO-STATE-1\x00"
const maxStoreBytes = 64 << 20

type diskState struct {
	Connections []map[string]any `json:"connections"`
	Settings    map[string]any   `json:"settings"`
	Sidebar     any              `json:"sidebar"`
	TableGroups map[string]any   `json:"tableGroups"`
	History     []map[string]any `json:"history"`
	SQLFiles    []map[string]any `json:"sqlFiles"`
	SQLFolders  []map[string]any `json:"sqlFolders"`
}

func emptyState() diskState {
	return diskState{Connections: []map[string]any{}, Settings: map[string]any{}, TableGroups: map[string]any{}, History: []map[string]any{}, SQLFiles: []map[string]any{}, SQLFolders: []map[string]any{}}
}
func regularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("Data file must be a regular file")
	}
	return nil
}
func (s *Server) openStore() error {
	if err := os.MkdirAll(s.dataDir, 0700); err != nil {
		return fmt.Errorf("Cannot create Go data directory: %w", err)
	}
	info, err := os.Lstat(s.dataDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Go data directory must be a regular directory")
	}
	if err = os.Chmod(s.dataDir, 0700); err != nil {
		return err
	}
	lockPath := filepath.Join(s.dataDir, ".store.lock")
	if err := regularFile(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lockFile, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return errors.New("Cannot acquire Go data directory lock")
	}
	if err = lockStore(lockFile); err != nil {
		_ = lockFile.Close()
		return errors.New("This Go data directory is already in use; close the other instance or select a different data directory")
	}
	s.storeLock = lockFile
	keyPath := filepath.Join(s.dataDir, "credentials.key")
	statePath := filepath.Join(s.dataDir, "state.enc")
	var key []byte
	if err = regularFile(keyPath); errors.Is(err, os.ErrNotExist) {
		if _, stateErr := os.Lstat(statePath); !errors.Is(stateErr, os.ErrNotExist) {
			return errors.New("Encrypted state exists but credentials.key is missing; restore the original key")
		}
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return err
		}
		f, createErr := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if createErr != nil {
			return errors.New("Cannot create credential encryption key")
		}
		if _, err = f.Write(key); err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	} else if err != nil {
		return err
	} else {
		key, err = os.ReadFile(keyPath)
		if err != nil {
			return errors.New("Cannot read credential encryption key")
		}
		if err = os.Chmod(keyPath, 0600); err != nil {
			return err
		}
	}
	if len(key) != 32 {
		return errors.New("Invalid credential encryption key")
	}
	block, err := aes.NewCipher(key)
	for i := range key {
		key[i] = 0
	}
	if err != nil {
		return err
	}
	s.aead, err = cipher.NewGCM(block)
	if err != nil {
		return err
	}
	s.state = emptyState()
	if err = regularFile(statePath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err = os.Chmod(statePath, 0600); err != nil {
		return err
	}
	f, err := os.Open(statePath)
	if err != nil {
		return err
	}
	defer f.Close()
	bytes, err := io.ReadAll(io.LimitReader(f, maxStoreBytes+1))
	if err != nil {
		return err
	}
	if len(bytes) > maxStoreBytes {
		return errors.New("Saved state exceeds size limit")
	}
	if !strings.HasPrefix(string(bytes), storeHeader) {
		return errors.New("Invalid Go state file; Rust storage is not imported automatically")
	}
	bytes = bytes[len(storeHeader):]
	if len(bytes) < s.aead.NonceSize() {
		return errors.New("Invalid encrypted state")
	}
	plaintext, err := s.aead.Open(nil, bytes[:s.aead.NonceSize()], bytes[s.aead.NonceSize():], []byte(storeHeader))
	if err != nil {
		return errors.New("Cannot decrypt saved state with credentials.key")
	}
	if err = json.Unmarshal(plaintext, &s.state); err != nil {
		return errors.New("Invalid saved state")
	}
	if s.state.Settings == nil {
		s.state.Settings = map[string]any{}
	}
	if s.state.TableGroups == nil {
		s.state.TableGroups = map[string]any{}
	}
	return nil
}

// update persists a private copy first so a failed disk write never changes live state.
func (s *Server) update(fn func(*diskState) error) error {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	s.stateMu.RLock()
	next, err := cloneDiskState(s.state)
	accessVersion := s.cacheAccessVersion
	s.stateMu.RUnlock()
	if err != nil {
		return err
	}
	if err = fn(&next); err != nil {
		return err
	}
	bytes, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if len(bytes) > maxStoreBytes-1024 {
		return errors.New("Saved state exceeds size limit")
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	encrypted := append([]byte(storeHeader), nonce...)
	encrypted = s.aead.Seal(encrypted, nonce, bytes, []byte(storeHeader))
	temporary, err := os.CreateTemp(s.dataDir, ".state-*")
	if err != nil {
		return errors.New("Cannot save Go application state")
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0600); err == nil {
		_, err = temporary.Write(encrypted)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return errors.New("Cannot save Go application state")
	}
	if err = os.Rename(temporaryPath, filepath.Join(s.dataDir, "state.enc")); err != nil {
		return errors.New("Cannot publish Go application state")
	}
	if directory, err := os.Open(s.dataDir); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	s.stateMu.Lock()
	// Cache reads can update recency while this private copy is being saved.
	// Retain those newer timestamps in memory; a later write or Close flushes them.
	mergeCacheAccesses(&next, s.state)
	s.state = next
	s.cacheAccessPersisted = accessVersion
	s.stateMu.Unlock()
	return nil
}
func (s *Server) snapshot() diskState {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	next, err := cloneDiskState(s.state)
	if err != nil {
		return emptyState()
	}
	return next
}
func walkSecrets(value any, visit func(map[string]any, string, string)) {
	switch v := value.(type) {
	case map[string]any:
		for key, value := range v {
			canonical := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
			sensitive := strings.Contains(canonical, "password") || strings.Contains(canonical, "passphrase") || strings.Contains(canonical, "secret") || strings.HasSuffix(canonical, "token") || canonical == "apikey" || canonical == "privatekey" || canonical == "credential"
			if text, ok := value.(string); ok && sensitive {
				visit(v, key, text)
			} else {
				walkSecrets(value, visit)
			}
		}
	case []any:
		for _, entry := range v {
			walkSecrets(entry, visit)
		}
	}
}
func cleanConnection(config map[string]any) map[string]any {
	copy := cloneMap(config)
	if save, ok := copy["save_password"].(bool); ok && !save {
		walkSecrets(copy, func(parent map[string]any, key, value string) { parent[key] = "" })
	}
	return copy
}
func upsert(entries []map[string]any, item map[string]any) []map[string]any {
	for i, existing := range entries {
		if str(existing, "id") == str(item, "id") {
			entries[i] = item
			return entries
		}
	}
	return append(entries, item)
}
func removeID(entries []map[string]any, id string) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if str(entry, "id") != id {
			out = append(out, entry)
		}
	}
	return out
}
