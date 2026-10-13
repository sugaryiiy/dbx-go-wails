package database

import (
	"net/url"
	"path/filepath"
	"strings"
)

// sqliteFileURL converts an absolute native file path to a SQLite file URI.
// Windows drive paths need a leading slash; otherwise net/url serializes the
// drive and escaped backslashes as an authority, which SQLite rejects.
func sqliteFileURL(absolutePath string) url.URL {
	path := filepath.ToSlash(absolutePath)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return url.URL{Scheme: "file", Path: path}
}
