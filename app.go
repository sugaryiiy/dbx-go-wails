//go:build desktop || bindings

package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/sugaryiiy/magpiedb/internal/database"
	"github.com/sugaryiiy/magpiedb/internal/desktopfiles"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App exposes native desktop capabilities only. Database operations continue
// through the shared Go HTTP handler inside Wails' in-process asset server.
type App struct {
	ctx           context.Context
	alwaysOnTop   atomic.Bool
	frontendReady atomic.Bool
	trayVisible   atomic.Bool
	backend       http.Handler
	saveDialog    func(runtime.SaveDialogOptions) (string, error)
	fileOnce      sync.Once
	fileGrants    *desktopfiles.Grants
}

func desktopAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Wails v2.16 assigns this RFC 5737 sentinel to native WebView
		// requests. Preserve real network addresses so the desktop server
		// continues to reject HTTP clients, including Wails' dev server.
		if r.RemoteAddr == "192.0.2.1:1234" {
			r = r.Clone(r.Context())
			r.RemoteAddr = ""
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	installTray(a)
}

type WindowState struct {
	Maximized   bool `json:"maximized"`
	Fullscreen  bool `json:"fullscreen"`
	AlwaysOnTop bool `json:"alwaysOnTop"`
}

func (a *App) GetWindowState() WindowState {
	return WindowState{runtime.WindowIsMaximised(a.ctx), runtime.WindowIsFullscreen(a.ctx), a.alwaysOnTop.Load()}
}
func (a *App) Minimize()       { runtime.WindowMinimise(a.ctx) }
func (a *App) ToggleMaximize() { runtime.WindowToggleMaximise(a.ctx) }
func (a *App) SetAlwaysOnTop(value bool) {
	runtime.WindowSetAlwaysOnTop(a.ctx, value)
	a.alwaysOnTop.Store(value)
}
func (a *App) SetTheme(mode string) error {
	if mode != "light" && mode != "dark" && mode != "system" {
		return fmt.Errorf("invalid window theme: %q", mode)
	}
	setWindowTheme(a.ctx, mode)
	return nil
}
func (a *App) Close() { runtime.Quit(a.ctx) }

// MarkFrontendReady is called by the frontend once its close-request listener
// is installed. Until then a native window close falls back to quitting, so a
// failed WebView start cannot leave a hidden app waiting forever.
func (a *App) MarkFrontendReady() { a.frontendReady.Store(true) }

// CompleteAppClose executes the close decision the frontend reached after its
// own confirmations (unsaved tabs, blocking AI runs, quit-vs-hide choice).
// "quit" exits the process; "hide" backgrounds the window.
func (a *App) CompleteAppClose(action string) {
	switch action {
	case "quit":
		runtime.Quit(a.ctx)
	case "hide":
		a.hideMainWindow()
	}
}

// RequestAppClose asks the frontend to start its close flow, exactly as if
// the user had clicked the window's native close button.
func (a *App) RequestAppClose() { a.emitCloseRequested("settings") }

// RequestAppQuit asks the frontend to start its close flow with the quit
// action preselected. The tray menu's Quit item uses it so quitting from the
// tray still honours unsaved-tab and AI-run confirmations.
func (a *App) RequestAppQuit() { a.emitCloseRequested("quit") }

func (a *App) emitCloseRequested(target string) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "dbx-app-close-requested", target)
}

// SetTrayVisible shows or hides the tray / menu bar icon, driven by the
// frontend's "show tray icon" desktop setting. Platforms without a tray
// implementation ignore it.
func (a *App) SetTrayVisible(visible bool) {
	a.trayVisible.Store(visible)
	setTrayVisible(visible)
}

// beforeClose intercepts the native window close. The frontend owns every
// close decision, so the shell only forwards the request and keeps the window
// alive until the frontend answers through CompleteAppClose.
func (a *App) beforeClose(ctx context.Context) bool {
	if !a.frontendReady.Load() {
		return false
	}
	runtime.EventsEmit(ctx, "dbx-app-close-requested", "settings")
	return true
}

func (a *App) hideMainWindow() {
	// With a visible tray icon the window can hide completely and be restored
	// from the tray. Without one, minimise instead so the window always stays
	// reachable through the Dock or taskbar.
	if trayAvailable() && a.trayVisible.Load() {
		runtime.WindowHide(a.ctx)
		return
	}
	runtime.WindowMinimise(a.ctx)
}

func (a *App) showMainWindow() {
	runtime.WindowUnminimise(a.ctx)
	runtime.WindowShow(a.ctx)
}
func (a *App) ClipboardReadText() (string, error)   { return runtime.ClipboardGetText(a.ctx) }
func (a *App) ClipboardWriteText(text string) error { return runtime.ClipboardSetText(a.ctx, text) }

type FileFilter struct {
	Name       string   `json:"name"`
	Extensions []string `json:"extensions"`
}
type FileDialogOptions struct {
	Title       string       `json:"title"`
	DefaultPath string       `json:"defaultPath"`
	Directory   bool         `json:"directory"`
	Multiple    bool         `json:"multiple"`
	Filters     []FileFilter `json:"filters"`
}

func nativeFilters(filters []FileFilter) []runtime.FileFilter {
	result := make([]runtime.FileFilter, 0, len(filters))
	for _, filter := range filters {
		patterns := make([]string, 0, len(filter.Extensions))
		for _, extension := range filter.Extensions {
			if extension == "*" {
				patterns = append(patterns, "*")
			} else {
				patterns = append(patterns, "*."+strings.TrimPrefix(extension, "."))
			}
		}
		result = append(result, runtime.FileFilter{DisplayName: filter.Name, Pattern: strings.Join(patterns, ";")})
	}
	return result
}

func (a *App) OpenFile(options FileDialogOptions) ([]string, error) {
	native := runtime.OpenDialogOptions{Title: options.Title, Filters: nativeFilters(options.Filters)}
	if options.DefaultPath != "" {
		if options.Directory {
			native.DefaultDirectory = options.DefaultPath
		} else {
			native.DefaultDirectory, native.DefaultFilename = filepath.Split(options.DefaultPath)
		}
	}
	if options.Multiple && !options.Directory {
		paths, err := runtime.OpenMultipleFilesDialog(a.ctx, native)
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			if err = a.files().Grant(path, false); err != nil {
				return nil, err
			}
		}
		return paths, nil
	}
	var selected string
	var err error
	if options.Directory {
		selected, err = runtime.OpenDirectoryDialog(a.ctx, native)
	} else {
		selected, err = runtime.OpenFileDialog(a.ctx, native)
	}
	if err != nil || selected == "" {
		return []string{}, err
	}
	if err = a.files().Grant(selected, options.Directory); err != nil {
		return nil, err
	}
	return []string{selected}, nil
}

func (a *App) SaveFile(options FileDialogOptions) (string, error) {
	directory, filename := filepath.Split(options.DefaultPath)
	return a.chooseSaveFile(runtime.SaveDialogOptions{Title: options.Title, DefaultDirectory: directory, DefaultFilename: filename, Filters: nativeFilters(options.Filters)})
}

func (a *App) chooseSaveFile(options runtime.SaveDialogOptions) (string, error) {
	var path string
	var err error
	if a.saveDialog != nil {
		path, err = a.saveDialog(options)
	} else {
		path, err = runtime.SaveFileDialog(a.ctx, options)
	}
	if err == nil && path != "" {
		err = a.files().Grant(path, false)
	}
	return path, err
}

func (a *App) files() *desktopfiles.Grants {
	a.fileOnce.Do(func() { a.fileGrants = desktopfiles.New() })
	return a.fileGrants
}
func (a *App) ReadExternalSQL(path string, maxBytes int) (desktopfiles.Snapshot, error) {
	return a.files().Read(path, maxBytes)
}
func (a *App) InspectExternalSQL(path string) (desktopfiles.Status, error) {
	return a.files().Inspect(path)
}
func (a *App) WriteExternalSQL(path, content string, options desktopfiles.WriteOptions) (desktopfiles.WriteResult, error) {
	return a.files().Write(path, content, options)
}

type SavedExternalSQL struct {
	Path    string               `json:"path"`
	Version desktopfiles.Version `json:"version"`
}

func (a *App) SaveExternalSQL(name, content, extension string) (*SavedExternalSQL, error) {
	if extension == "" {
		extension = "sql"
	}
	path, err := a.chooseSaveFile(runtime.SaveDialogOptions{Title: "Save SQL file", DefaultFilename: filepath.Base(name), Filters: nativeFilters([]FileFilter{{Name: "SQL", Extensions: []string{extension}}})})
	if err != nil || path == "" {
		return nil, err
	}
	result, err := a.files().Write(path, content, desktopfiles.WriteOptions{})
	if err != nil {
		return nil, err
	}
	if result.Kind != "written" || result.Version == nil {
		return nil, fmt.Errorf("SQL file changed while saving; retry")
	}
	return &SavedExternalSQL{Path: path, Version: *result.Version}, nil
}
func (a *App) ListSQLFiles(root, filter string) ([]desktopfiles.Entry, error) {
	return a.files().List(root, filter)
}
func (a *App) CreateSQLFile(root, directory, name string) (string, error) {
	return a.files().Create(root, directory, name)
}
func (a *App) RenameSQLFile(root, path, name string) (string, error) {
	return a.files().Rename(root, path, name)
}
func (a *App) DeleteSQLFile(root, path string) error { return a.files().Delete(root, path) }

func (a *App) IsSQLiteDatabaseFile(path string) (bool, error) {
	real, err := a.files().Path(path)
	if err != nil {
		return false, err
	}
	return database.IsSQLiteFile(real)
}
func (a *App) BackupSQLiteDatabase(id, path string) error {
	real, err := a.files().Path(path)
	if err != nil {
		return err
	}
	provider, ok := a.backend.(interface {
		SQLiteBackup(context.Context, string, string) error
	})
	if !ok {
		return fmt.Errorf("desktop backup backend is unavailable")
	}
	return provider.SQLiteBackup(a.ctx, id, real)
}
func (a *App) RestoreSQLiteDatabase(id, path string) error {
	real, err := a.files().Path(path)
	if err != nil {
		return err
	}
	provider, ok := a.backend.(interface {
		SQLiteRestore(context.Context, string, string) error
	})
	if !ok {
		return fmt.Errorf("desktop restore backend is unavailable")
	}
	return provider.SQLiteRestore(a.ctx, id, real)
}

func (a *App) SaveFileBytes(defaultFilename, contentBase64 string) (string, error) {
	content, err := base64.StdEncoding.DecodeString(contentBase64)
	if err != nil {
		return "", fmt.Errorf("invalid file contents: %w", err)
	}
	path, err := a.chooseSaveFile(runtime.SaveDialogOptions{Title: "Save export", DefaultFilename: filepath.Base(defaultFilename)})
	if err != nil || path == "" {
		return "", err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".dbx-export-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(temp.Name())
	_, writeErr := temp.Write(content)
	closeErr := temp.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err = os.Rename(temp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

// SaveQueryResultExport downloads from the in-process API after the user has
// selected a destination. A failed export never truncates an existing file.
func (a *App) SaveQueryResultExport(exportID, defaultFilename string) (string, error) {
	path, err := a.chooseSaveFile(runtime.SaveDialogOptions{Title: "Save query result", DefaultFilename: filepath.Base(defaultFilename)})
	if err != nil || path == "" {
		return "", err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".dbx-export-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(temp.Name())
	writer := &exportFileWriter{file: temp, header: make(http.Header)}
	request, err := http.NewRequest(http.MethodGet, "/api/export/query-result/download/"+url.PathEscape(exportID), nil)
	if err != nil {
		temp.Close()
		return "", err
	}
	a.backend.ServeHTTP(writer, request)
	closeErr := temp.Close()
	if writer.status < 200 || writer.status >= 300 {
		return "", fmt.Errorf("export download failed (HTTP %d)", writer.status)
	}
	if writer.err != nil {
		return "", writer.err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

type exportFileWriter struct {
	file   *os.File
	header http.Header
	status int
	err    error
}

func (w *exportFileWriter) Header() http.Header { return w.header }
func (w *exportFileWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *exportFileWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.file.Write(data)
	if err != nil {
		w.err = err
	}
	return n, err
}
