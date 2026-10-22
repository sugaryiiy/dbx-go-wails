// dbx-mcp-go serves the Go application's MCP tools over stdio.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/sugaryiiy/magpiedb/internal/mcpbridge"
	"github.com/sugaryiiy/magpiedb/internal/server"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, flag.ErrHelp) {
		// SDK/transport errors may include URLs or server-provided text. Do not
		// allow a remote response to reflect the caller's token into diagnostics.
		message := err.Error()
		if token := os.Getenv("DBX_MCP_TOKEN"); token != "" {
			message = strings.ReplaceAll(message, token, "[redacted]")
		}
		fmt.Fprintln(os.Stderr, "dbx-mcp-go:", message)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("dbx-mcp-go", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", "", "Application data directory; requires exclusive access (close DBX first)")
	endpoint := flags.String("url", "", "Running DBX Streamable HTTP MCP endpoint; token comes from DBX_MCP_TOKEN")
	version := flags.Bool("version", false, "Print CLI version to stderr")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: dbx-mcp-go [--data-dir DIRECTORY] | --url http://127.0.0.1:PORT/mcp")
		fmt.Fprintln(stderr, "Serves MCP on stdin/stdout. Use --url while the desktop application is running.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("Unexpected positional arguments")
	}
	if *version {
		fmt.Fprintln(stderr, "dbx-mcp-go", mcpbridge.Version)
		return nil
	}
	if *endpoint != "" {
		if *dataDir != "" {
			return errors.New("Use either --url or --data-dir")
		}
		return mcpbridge.RunHTTPStdio(ctx, *endpoint, os.Getenv("DBX_MCP_TOKEN"))
	}
	application, err := server.New(server.Options{DataDir: *dataDir, Desktop: true, DisableMCPListener: true})
	if err != nil {
		return fmt.Errorf("Open DBX data: %w; if DBX is running, use --url with its MCP endpoint instead", err)
	}
	defer application.Close()
	return mcpbridge.RunStdio(ctx, application.MCPBackend())
}
