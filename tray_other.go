//go:build (desktop || bindings) && !darwin

package main

// Only macOS has a tray implementation so far. On other platforms the close
// flow still applies, but "hide" degrades to minimising the window (see
// hideMainWindow) because there is no tray icon to restore it from.
func trayAvailable() bool { return false }

func installTray(app *App) {}

func setTrayVisible(visible bool) {}
