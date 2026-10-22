//go:build desktop

package main

import (
	"embed"
	"io/fs"
	"log"
	"os"

	"github.com/sugaryiiy/dbx-go-wails/internal/server"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

// The production application carries its Vue UI; no Rust service or external
// HTTP listener is required. Run pnpm build:wails:ui before a direct Go build.
//
//go:embed all:dist
var frontendAssets embed.FS

func main() {
	assets, err := fs.Sub(frontendAssets, "dist")
	if err != nil {
		log.Fatal(err)
	}
	backend, err := server.New(server.Options{DataDir: os.Getenv("DBX_DATA_DIR"), Desktop: true})
	if err != nil {
		log.Fatal(err)
	}
	defer backend.Close()
	app := &App{backend: backend}
	err = wails.Run(&options.App{
		Title: "喜鹊 · MagpieDB", Width: 1440, Height: 900, MinWidth: 960, MinHeight: 640,
		// Keep the operating system's native title bar until custom titlebar and
		// detached-window parity are available on every desktop platform.
		Frameless:     false,
		AssetServer:   &assetserver.Options{Assets: assets, Handler: desktopAPI(backend)},
		OnStartup:     app.startup,
		OnBeforeClose: app.beforeClose,
		Bind:          []interface{}{app},
	})
	if err != nil {
		log.Fatal(err)
	}
}
