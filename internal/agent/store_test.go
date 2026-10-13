package agent

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func jarBytes(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	manifest, err := writer.Create("META-INF/MANIFEST.MF")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = manifest.Write([]byte("Manifest-Version: 1.0\nMain-Class: com.dbx.Fixture\n"))
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
func checksum(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func writePackage(t *testing.T, registry Registry, entries map[string][]byte) string {
	t.Helper()
	source := filepath.Join(t.TempDir(), "drivers.zip")
	file, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	data, _ := json.Marshal(registry)
	entries["agent-registry.json"] = data
	for name, data := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = entry.Write(data)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	file.Close()
	return source
}
func TestOfflineImportVerifiesIntegrityAndPreservesExistingDriver(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	jar := jarBytes(t)
	artifact := Artifact{URL: "fixture.jar", Size: int64(len(jar)), SHA256: checksum(jar)}
	registry := Registry{Drivers: map[string]DriverInfo{"h2": {Version: "1", JAR: &artifact}}}
	source := writePackage(t, registry, map[string][]byte{"drivers/fixture.jar": jar})
	result, err := store.ImportPackage(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if result["count"] != 1 {
		t.Fatalf("%v", result)
	}
	before, err := os.ReadFile(filepath.Join(store.Root, "drivers", "h2", "agent.jar"))
	if err != nil {
		t.Fatal(err)
	}
	registry.Drivers["h2"] = DriverInfo{Version: "2", JAR: &Artifact{URL: "fixture.jar", Size: int64(len(jar)), SHA256: "0000000000000000000000000000000000000000000000000000000000000000"}}
	source = writePackage(t, registry, map[string][]byte{"drivers/fixture.jar": jar})
	result, err = store.ImportPackage(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if result["count"] != 0 || len(result["failures"].([]map[string]any)) != 1 {
		t.Fatalf("checksum mismatch reported success: %v", result)
	}
	after, _ := os.ReadFile(filepath.Join(store.Root, "drivers", "h2", "agent.jar"))
	if !bytes.Equal(before, after) {
		t.Fatal("bad import replaced existing driver")
	}
	reopened, err := NewStore(store.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.state.Drivers["h2"].Version != "1" {
		t.Fatal("failed import overwrote durable state")
	}
}
func TestArchiveRejectsTraversalSymlinksAndDuplicateNames(t *testing.T) {
	for _, name := range []string{"../escaped", "/absolute", "drivers/../../escaped", "C:\\evil", "drivers\\evil"} {
		t.Run(name, func(t *testing.T) {
			var data bytes.Buffer
			writer := zip.NewWriter(&data)
			entry, _ := writer.Create(name)
			_, _ = entry.Write([]byte("bad"))
			writer.Close()
			source := filepath.Join(t.TempDir(), "bad.zip")
			os.WriteFile(source, data.Bytes(), 0600)
			if err := extractArchive(context.Background(), source, t.TempDir(), "", source); err == nil {
				t.Fatal("unsafe path accepted")
			}
		})
	}
	for _, kind := range []string{"symlink", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "bad.tar.zst")
			file, _ := os.Create(source)
			z, _ := zstd.NewWriter(file)
			writer := tar.NewWriter(z)
			header := &tar.Header{Name: "agent", Mode: 0644, Size: 1, Typeflag: tar.TypeReg}
			if kind == "symlink" {
				header.Typeflag = tar.TypeSymlink
				header.Linkname = "../escape"
				header.Size = 0
			}
			writer.WriteHeader(header)
			if header.Size > 0 {
				writer.Write([]byte("x"))
			}
			if kind == "duplicate" {
				writer.WriteHeader(header)
				writer.Write([]byte("y"))
			}
			writer.Close()
			z.Close()
			file.Close()
			if err := extractArchive(context.Background(), source, t.TempDir(), "tar_zstd", source); err == nil {
				t.Fatal("unsafe entry accepted")
			}
		})
	}
}
func TestImportDriverRejectsInvalidJarAndScripts(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source := filepath.Join(t.TempDir(), "bad")
	os.WriteFile(source, []byte("#!/bin/sh\necho fake-success\n"), 0700)
	for _, name := range []string{"bad.jar", "agent"} {
		if err := store.ImportDriver(context.Background(), "h2", source, name); err == nil {
			t.Fatal("invalid imported agent accepted")
		}
	}
	if err := store.Uninstall("../../escape"); err == nil {
		t.Fatal("unsafe uninstall accepted")
	}
}
func TestCatalogProfilesMatchPackageAliases(t *testing.T) {
	if key, ok := Key("influxdb", "influxdb"); ok || key != "" || UsesDatabase("influxdb") {
		t.Fatal("native HTTP InfluxDB incorrectly requires a standalone agent")
	}
	if _, listed := Labels()["influxdb"]; listed {
		t.Fatal("InfluxDB advertised as a downloadable standalone agent")
	}
	cases := map[string]string{"h2:h2-v1": "h2", "gbase:gbase8s": "gbase8s", "etcd:etcd-v2": "etcd2", "iris:cache": "cache", "nebula:nebula-v3": "nebula", "sqlserver:": "sqlserver-legacy"}
	for pair, want := range cases {
		parts := bytes.Split([]byte(pair), []byte(":"))
		got, ok := Key(string(parts[0]), string(parts[1]))
		if !ok || got != want {
			t.Errorf("%s=%s want %s", pair, got, want)
		}
	}
}

func TestDriverStateCommitFailureRestoresArtifactAndMemory(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source := filepath.Join(t.TempDir(), "driver.jar")
	before := jarBytes(t)
	if err := os.WriteFile(source, before, 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.commitDriver(context.Background(), "h2", DriverInfo{Version: "1"}, source, false); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(store.Root, "state.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(store.Root, "state.json"), 0700); err != nil {
		t.Fatal(err)
	}
	after := append(jarBytes(t), []byte("new bytes")...)
	if err := os.WriteFile(source, after, 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.commitDriver(context.Background(), "h2", DriverInfo{Version: "2"}, source, false); err == nil {
		t.Fatal("state save failure reported success")
	}
	restored, err := os.ReadFile(filepath.Join(store.Root, "drivers", "h2", "agent.jar"))
	if err != nil || !bytes.Equal(restored, before) || store.state.Drivers["h2"].Version != "1" {
		t.Fatalf("artifact/state not rolled back: %v, %v", err, store.state)
	}
}
