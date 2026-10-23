//go:build desktop || bindings

package main

import (
	"context"
	"fmt"

	"github.com/sugaryiiy/magpiedb/internal/server"
	"github.com/sugaryiiy/magpiedb/internal/updater"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// The update bindings are the desktop-only half of the update contract:
// checking runs over the shared HTTP API (it also works in web mode, as
// manual-only), but downloading, caching and installing packages are local
// file operations that only the desktop shell performs.

func (a *App) updateService() *updater.Service {
	a.updateOnce.Do(func() {
		if srv, ok := a.backend.(*server.Server); ok {
			a.updates = srv.UpdateService()
		}
	})
	return a.updates
}

// DownloadUpdate resolves the release for version, downloads this
// platform's package into the update cache and verifies its SHA-256
// against the GitHub asset digest (or .sha256 sidecar). Progress is
// streamed to the frontend as "update-download-progress" events.
func (a *App) DownloadUpdate(source string, version string, attemptID string, releaseNotes string) (*updater.DownloadedUpdate, error) {
	service := a.updateService()
	if service == nil {
		return nil, fmt.Errorf("update downloads are unavailable in this build")
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return service.Download(ctx, version, attemptID, releaseNotes, func(progress updater.DownloadProgress) {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "update-download-progress", progress)
		}
	})
}

// CancelUpdateDownload aborts the in-flight update download, if any.
func (a *App) CancelUpdateDownload() {
	if service := a.updateService(); service != nil {
		service.Cancel()
	}
}

// GetDownloadedUpdate returns the newest complete cached update package,
// or nil when none is ready.
func (a *App) GetDownloadedUpdate() (*updater.DownloadedUpdate, error) {
	service := a.updateService()
	if service == nil {
		return nil, nil
	}
	return service.Downloaded()
}

// DiscardDownloadedUpdate removes a cached update package (superseded or
// ignored updates).
func (a *App) DiscardDownloadedUpdate(cacheID string) error {
	service := a.updateService()
	if service == nil {
		return nil
	}
	return service.Discard(cacheID)
}

// InstallDownloadedUpdate stages a verified cached package, starts the
// detached update helper and drives the normal quit flow (the same one
// the tray Quit item uses). The helper waits for this process to exit,
// swaps the package, and relaunches the new build; if the swap fails it
// restores the current build and records the failure for the dialog.
func (a *App) InstallDownloadedUpdate(cacheID string, expectedVersion string) error {
	service := a.updateService()
	if service == nil {
		return fmt.Errorf("update installation is unavailable in this build")
	}
	jobPath, err := service.PrepareInstall(cacheID, expectedVersion)
	if err != nil {
		return err
	}
	if err := updater.SpawnHelper(jobPath); err != nil {
		return fmt.Errorf("start update helper: %w", err)
	}
	a.emitCloseRequested("quit")
	return nil
}

// GetUpdateFailure returns the version of a failed update that was rolled
// back, or "" when there is nothing to report.
func (a *App) GetUpdateFailure() string {
	service := a.updateService()
	if service == nil {
		return ""
	}
	return service.PendingFailure()
}

// ClearUpdateFailure dismisses a reported update failure and removes its
// leftovers.
func (a *App) ClearUpdateFailure() error {
	service := a.updateService()
	if service == nil {
		return nil
	}
	return service.ClearPendingFailure()
}
