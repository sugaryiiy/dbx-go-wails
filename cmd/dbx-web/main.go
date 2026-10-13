// Command dbx-web serves the Vue workbench with the Go backend.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sugaryiiy/dbx-go-wails/internal/server"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func run() error {
	bind := flag.String("bind", env("DBX_BIND_ADDR", "127.0.0.1"), "HTTP bind address")
	port := flag.String("port", env("DBX_PORT", "4224"), "HTTP port")
	dir := flag.String("data-dir", env("DBX_DATA_DIR", ".dbx-dev/go"), "Isolated Go application data directory")
	assets := flag.String("assets", env("DBX_STATIC_DIR", "dist"), "Built Vue assets")
	basePath := flag.String("base-path", env("DBX_PUBLIC_BASE_PATH", ""), "Web mount path (must match the frontend build base)")
	flag.Parse()
	dataDir, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dataDir, 0700); err != nil {
		return err
	}
	password := os.Getenv("DBX_PASSWORD")
	if password == "" {
		filename := filepath.Join(dataDir, "login-password")
		data, err := os.ReadFile(filename)
		if errors.Is(err, os.ErrNotExist) {
			var random [32]byte
			if _, err = rand.Read(random[:]); err != nil {
				return err
			}
			data = []byte(base64.RawURLEncoding.EncodeToString(random[:]))
			f, e := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e != nil {
				return e
			}
			_, err = f.Write(data)
			closeErr := f.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		} else if err != nil {
			return err
		}
		password = strings.TrimRight(string(data), "\r\n")
		if password == "" {
			return errors.New("the password file is empty")
		}
		log.Printf("Login password is stored in %s", filename)
	}
	app, err := server.New(server.Options{DataDir: dataDir, Password: password, Assets: os.DirFS(*assets), BasePath: *basePath})
	if err != nil {
		return err
	}
	defer app.Close()
	listener, err := net.Listen("tcp", net.JoinHostPort(*bind, *port))
	if err != nil {
		return err
	}
	httpServer := &http.Server{Handler: app, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	failed := make(chan error, 1)
	go func() { failed <- httpServer.Serve(listener) }()
	log.Printf("DBX Go backend ready at http://%s; data: %s", listener.Addr(), dataDir)
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdown); err != nil {
			_ = httpServer.Close()
		}
		return nil
	case err := <-failed:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("HTTP server: %w", err)
	}
}
