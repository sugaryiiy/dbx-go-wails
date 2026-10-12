//go:build (desktop || bindings) && darwin

package main

/*
#cgo LDFLAGS: -framework Cocoa
void dbx_set_window_theme(int theme);
*/
import "C"

import "context"

// Wails v2's macOS WindowSet*Theme methods are empty. Apply the Cocoa
// appearance on the main queue instead, including native dialogs and menus.
func setWindowTheme(_ context.Context, mode string) {
	var theme C.int
	switch mode {
	case "light":
		theme = 1
	case "dark":
		theme = 2
	}
	C.dbx_set_window_theme(theme)
}
