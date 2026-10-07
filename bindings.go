//go:build bindings && !desktop

package main

import (
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
)

// The Wails binding generator deliberately removes the desktop build tag.
// Reflection needs only the bridge methods, without opening a database or GUI.
func main() {
	if err := wails.Run(&options.App{Bind: []interface{}{&App{}}}); err != nil {
		panic(err)
	}
}
