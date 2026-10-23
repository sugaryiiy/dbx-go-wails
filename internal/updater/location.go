package updater

import (
	"os"
	"path/filepath"
	"strings"
)

// InstallLocationWritable reports whether the running build's install
// location can be replaced by the update helper. On macOS the writable
// unit is the .app bundle's parent directory; installs running from a DMG
// mount or an App Translocation path are never writable. Other platforms
// check the executable's own directory.
func InstallLocationWritable(executablePath, goos string) bool {
	if executablePath == "" {
		return false
	}
	target := executablePath
	if goos == "darwin" {
		if strings.Contains(executablePath, "AppTranslocation") || strings.HasPrefix(executablePath, "/Volumes/") {
			return false
		}
		dir := filepath.Dir(executablePath)
		for {
			if strings.HasSuffix(dir, ".app") {
				target = filepath.Dir(dir)
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				target = filepath.Dir(executablePath)
				break
			}
			dir = parent
		}
	} else {
		target = filepath.Dir(executablePath)
	}
	return DirectoryWritable(target)
}

// DirectoryWritable probes a directory with a real create/remove cycle;
// permission bits alone lie for ACL'd system directories.
func DirectoryWritable(dir string) bool {
	if dir == "" {
		return false
	}
	probe, err := os.CreateTemp(dir, ".magpiedb-update-probe-*")
	if err != nil {
		return false
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return true
}
