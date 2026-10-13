// Package desktopfiles grants filesystem access only to native picker choices.
package desktopfiles

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

const MaxSQLBytes = 32 << 20

type Version struct {
	SizeBytes   int64  `json:"sizeBytes"`
	ModifiedNS  string `json:"modifiedNs"`
	ContentHash string `json:"contentHash"`
}
type Snapshot struct {
	Content string  `json:"content"`
	Version Version `json:"version"`
}
type Status struct {
	Kind       string `json:"kind"`
	SizeBytes  int64  `json:"sizeBytes,omitempty"`
	ModifiedNS string `json:"modifiedNs,omitempty"`
}
type WriteOptions struct {
	ExpectedContentHash string `json:"expectedContentHash"`
	ExpectedMissing     bool   `json:"expectedMissing"`
}
type WriteResult struct {
	Kind           string   `json:"kind"`
	Version        *Version `json:"version,omitempty"`
	CurrentVersion *Version `json:"currentVersion,omitempty"`
}
type Entry struct {
	Name     string  `json:"name"`
	Path     string  `json:"path"`
	IsDir    bool    `json:"is_dir"`
	Children []Entry `json:"children"`
}
type Grants struct {
	mu          sync.Mutex
	files       map[string]bool
	directories map[string]bool
}

func New() *Grants { return &Grants{files: map[string]bool{}, directories: map[string]bool{}} }
func (g *Grants) Path(path string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.authorize(path)
}
func canonical(path string) (string, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return "", errors.New("file path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	parent, tail := absolute, []string{}
	for {
		real, e := filepath.EvalSymlinks(parent)
		if e == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				real = filepath.Join(real, tail[i])
			}
			return real, nil
		}
		if !errors.Is(e, os.ErrNotExist) {
			return "", e
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", e
		}
		tail = append(tail, filepath.Base(parent))
		parent = next
	}
}
func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func (g *Grants) Grant(path string, directory bool) error {
	real, err := canonical(path)
	if err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if directory {
		g.directories[real] = true
	} else {
		g.files[real] = true
	}
	return nil
}
func (g *Grants) authorize(path string) (string, error) {
	real, err := canonical(path)
	if err != nil {
		return "", err
	}
	if g.files[real] {
		return real, nil
	}
	for root := range g.directories {
		if inside(root, real) {
			return real, nil
		}
	}
	return "", errors.New("open this file or folder using the native picker before accessing it")
}
func read(path string, max int) (Snapshot, error) {
	out := Snapshot{}
	if max <= 0 || max > MaxSQLBytes {
		max = MaxSQLBytes
	}
	f, err := os.Open(path)
	if err != nil {
		return out, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return out, err
	}
	if !info.Mode().IsRegular() {
		return out, errors.New("SQL path must be a regular file")
	}
	if info.Size() > int64(max) {
		return out, errors.New("SQL file exceeds the preview size limit")
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil {
		return out, err
	}
	if len(b) > max {
		return out, errors.New("SQL file exceeds the preview size limit")
	}
	if !utf8.Valid(b) {
		return out, errors.New("SQL file must contain UTF-8 text")
	}
	latest, err := f.Stat()
	if err != nil {
		return out, err
	}
	if info.Size() != latest.Size() || !info.ModTime().Equal(latest.ModTime()) {
		return out, errors.New("SQL file changed while being read; retry")
	}
	h := sha256.Sum256(b)
	out.Content = strings.TrimPrefix(string(b), "\ufeff")
	out.Version = Version{SizeBytes: int64(len(b)), ModifiedNS: strconv.FormatInt(latest.ModTime().UnixNano(), 10), ContentHash: hex.EncodeToString(h[:])}
	return out, nil
}
func (g *Grants) Read(path string, max int) (Snapshot, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	real, err := g.authorize(path)
	if err != nil {
		return Snapshot{}, err
	}
	return read(real, max)
}
func (g *Grants) Inspect(path string) (Status, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	real, err := g.authorize(path)
	if err != nil {
		return Status{}, err
	}
	info, err := os.Stat(real)
	if errors.Is(err, os.ErrNotExist) {
		return Status{Kind: "missing"}, nil
	}
	if err != nil {
		return Status{}, err
	}
	if !info.Mode().IsRegular() {
		return Status{}, errors.New("SQL path must be a regular file")
	}
	return Status{Kind: "present", SizeBytes: info.Size(), ModifiedNS: strconv.FormatInt(info.ModTime().UnixNano(), 10)}, nil
}
func (g *Grants) Write(path, content string, opts WriteOptions) (WriteResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := WriteResult{}
	real, err := g.authorize(path)
	if err != nil {
		return out, err
	}
	if len(content) > MaxSQLBytes || !utf8.ValidString(content) {
		return out, errors.New("SQL content must be bounded UTF-8")
	}
	old, err := read(real, MaxSQLBytes)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return out, err
	}
	if !missing && (opts.ExpectedMissing || opts.ExpectedContentHash != "" && old.Version.ContentHash != opts.ExpectedContentHash) {
		return WriteResult{Kind: "conflict", CurrentVersion: &old.Version}, nil
	}
	if missing && opts.ExpectedContentHash != "" && !opts.ExpectedMissing {
		return WriteResult{Kind: "missing"}, nil
	}
	f, err := os.CreateTemp(filepath.Dir(real), ".dbx-sql-*")
	if err != nil {
		return out, err
	}
	defer os.Remove(f.Name())
	mode := os.FileMode(0600)
	if info, e := os.Stat(real); e == nil {
		mode = info.Mode().Perm()
	}
	if err = f.Chmod(mode); err == nil {
		_, err = io.WriteString(f, content)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return out, err
	}
	if closeErr != nil {
		return out, closeErr
	}
	current, e := read(real, MaxSQLBytes)
	if missing && !errors.Is(e, os.ErrNotExist) || !missing && (e != nil || current.Version.ContentHash != old.Version.ContentHash) {
		if e == nil {
			return WriteResult{Kind: "conflict", CurrentVersion: &current.Version}, nil
		}
		return WriteResult{Kind: "missing"}, nil
	}
	if err = os.Rename(f.Name(), real); err != nil {
		return out, err
	}
	saved, err := read(real, MaxSQLBytes)
	if err != nil {
		return out, err
	}
	return WriteResult{Kind: "written", Version: &saved.Version}, nil
}
func (g *Grants) List(root, filter string) ([]Entry, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	real, err := g.authorize(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return nil, errors.New("select a SQL folder")
	}
	count := 0
	var walk func(string, int) ([]Entry, error)
	walk = func(path string, depth int) ([]Entry, error) {
		if depth > 32 {
			return nil, errors.New("folder nesting exceeds 32")
		}
		items, err := os.ReadDir(path)
		if err != nil {
			return nil, err
		}
		entries := []Entry{}
		for _, item := range items {
			if item.Type()&os.ModeSymlink != 0 {
				continue
			}
			count++
			if count > 10000 {
				return nil, errors.New("folder contains more than 10000 entries")
			}
			name := item.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			entry := Entry{Name: name, Path: filepath.Join(path, name), IsDir: item.IsDir(), Children: []Entry{}}
			if entry.IsDir {
				entry.Children, err = walk(entry.Path, depth+1)
				if err != nil {
					return nil, err
				}
			} else if filter != "" {
				match := false
				for _, pattern := range strings.Split(filter, ",") {
					pattern = strings.TrimSpace(pattern)
					if strings.HasPrefix(pattern, ".") {
						pattern = "*" + pattern
					}
					if ok, _ := filepath.Match(pattern, name); ok {
						match = true
					}
				}
				if !match {
					continue
				}
			}
			entries = append(entries, entry)
		}
		sort.SliceStable(entries, func(i, j int) bool {
			if entries[i].IsDir != entries[j].IsDir {
				return entries[i].IsDir
			}
			return entries[i].Name < entries[j].Name
		})
		return entries, nil
	}
	return walk(real, 0)
}
func safeName(name string) error {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, "/\\\x00") {
		return errors.New("invalid SQL filename")
	}
	return nil
}
func (g *Grants) Create(root, directory, name string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := safeName(name); err != nil {
		return "", err
	}
	realRoot, err := g.authorize(root)
	if err != nil {
		return "", err
	}
	dir, err := g.authorize(directory)
	if err != nil {
		return "", err
	}
	if !inside(realRoot, dir) {
		return "", errors.New("directory is outside the selected root")
	}
	dest := filepath.Join(dir, name)
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	return dest, f.Close()
}
func (g *Grants) Rename(root, path, name string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := safeName(name); err != nil {
		return "", err
	}
	realRoot, err := g.authorize(root)
	if err != nil {
		return "", err
	}
	real, err := g.authorize(path)
	if err != nil {
		return "", err
	}
	if !inside(realRoot, real) || realRoot == real {
		return "", errors.New("file is outside the selected root")
	}
	info, err := os.Stat(real)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("only regular SQL files may be renamed")
	}
	dest := filepath.Join(filepath.Dir(real), name)
	if _, err = os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("destination already exists")
	}
	return dest, os.Rename(real, dest)
}
func (g *Grants) Delete(root, path string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	realRoot, err := g.authorize(root)
	if err != nil {
		return err
	}
	real, err := g.authorize(path)
	if err != nil {
		return err
	}
	if !inside(realRoot, real) || realRoot == real {
		return errors.New("file is outside the selected root")
	}
	info, err := os.Stat(real)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("only regular SQL files may be deleted")
	}
	return os.Remove(real)
}
