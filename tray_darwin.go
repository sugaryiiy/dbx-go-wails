//go:build (desktop || bindings) && darwin

package main

/*
#cgo LDFLAGS: -framework Cocoa

void dbx_tray_install(const void *iconPNG, int iconLen);
void dbx_tray_set_visible(int visible);
*/
import "C"

import (
	_ "embed"
	"unsafe"
)

// The menu bar icon reuses the bundled app icon.
//
//go:embed build/wails/appicon.png
var trayIconPNG []byte

// trayApp receives the callbacks the native status item menu raises.
var trayApp *App

func trayAvailable() bool { return true }

func installTray(app *App) {
	trayApp = app
	app.trayVisible.Store(true)
	if len(trayIconPNG) == 0 {
		C.dbx_tray_install(nil, 0)
		return
	}
	C.dbx_tray_install(unsafe.Pointer(&trayIconPNG[0]), C.int(len(trayIconPNG)))
}

func setTrayVisible(visible bool) {
	v := C.int(0)
	if visible {
		v = 1
	}
	C.dbx_tray_set_visible(v)
}

//export dbxTrayShowMainWindow
func dbxTrayShowMainWindow() {
	if trayApp != nil {
		trayApp.showMainWindow()
	}
}

//export dbxTrayRequestQuit
func dbxTrayRequestQuit() {
	if trayApp != nil {
		trayApp.RequestAppQuit()
	}
}
