# DBX Go + Wails

A Vue 3 database workbench with a native Go backend and a Wails desktop shell.
The Go entry points run without Cargo, Rust, Tauri, or an external backend process.
This is an active migration, **not yet feature-equivalent to the original DBX**.

[中文说明](README.zh-CN.md) · [Migration status](MIGRATION.md) · [Preserved Rust version](https://github.com/sugaryiiy/dbx-rust)

## Run

Requirements: Go 1.25+, Node.js 22.13+, pnpm 10.27.0. Desktop builds additionally
need the [Wails platform prerequisites](https://wails.io/docs/gettingstarted/installation).
The CLI is pinned to Wails 2.16.0 and downloaded by the repository script.

```sh
pnpm install --frozen-lockfile
pnpm dev            # Go HTTP backend + Vue development server
pnpm dev:wails      # debug build + native launch (no hot reload)
pnpm build:wails    # native desktop application
```

On macOS the application is generated at `build/wails/bin/DBX Go.app`.
The development web UI defaults to `http://127.0.0.1:5174`; its generated local
login password is in `.dbx-dev/go/login-password`. `DBX_GO_PORT`,
`DBX_GO_BACKEND_PORT` and `DBX_GO_DATA_DIR` customize the development instance.

For a standalone Go web application:

```sh
pnpm build:ui
pnpm build:go
./build/bin/dbx-web
```

It listens on `127.0.0.1:4224` by default. Set `DBX_PASSWORD` or use the generated
password file whose path is printed on startup. `DBX_DATA_DIR`, `DBX_PORT`,
`DBX_BIND_ADDR`, and `DBX_STATIC_DIR` configure the service. Keep its data directory
separate from both Rust DBX and any concurrently running Go instance.

## Current implementation

- Native SQLite, MySQL/MariaDB and PostgreSQL connections, queries, transactions,
  cancellation, timeouts and basic metadata.
- Connection profiles, saved SQL, query history, settings and layouts persisted
  with encrypted application state and exclusive process locking.
- A persistent SQLite sample database for trying the workbench.
- CSV/TSV/XLSX import with transactional parameterized writes; CSV/XLSX/JSON/text
  query export, progress and cancellation.
- Primary-key-based data grid edits, copied INSERT/UPDATE SQL and rollback SQL.
- Native Wails window controls, text clipboard, file dialogs and export saving.
- Go MCP with 13 database tools, Streamable HTTP/stdio, connection permissions,
  bearer tokens and query auditing. See [MCP setup](docs/go-mcp.md).

SQLite has real integration and HTTP round-trip coverage. MySQL/PostgreSQL have
not yet been validated against live servers. Import/export currently caps a job
at 100,000 rows and 32 MiB; exports execute read-only queries. TLS/SSH, additional
database engines, advanced schema editing, AI, plugins and releases remain
in migration. Unsupported backend operations return an explicit error.
See [MIGRATION.md](MIGRATION.md) for limitations and validation evidence.

## MCP

Open Settings → MCP, apply connection/tool permissions, then enable the service.
Desktop defaults to `http://127.0.0.1:5225/mcp`; Web uses `/mcp` on its existing
server. Copy a client template and supply the generated bearer token.
`pnpm build:mcp` builds the `dbx-mcp-go` stdio CLI; use `--url` while DBX is running.
See [the MCP guide](docs/go-mcp.md) for configuration and supported SQL limits.

## Verify

```sh
pnpm test:go
pnpm test:go-smoke  # creates and cleans up an isolated HTTP instance
go test -mod=mod -race ./internal/... ./cmd/...
# After building the Vue assets:
go test -mod=mod -tags desktop .
```

The active GitHub workflow validates Go on Linux/macOS/Windows and builds Wails
on macOS. Upstream workflows are retained, disabled, in `.github/legacy-workflows`.
Legacy Rust sources and explicit `legacy:*` scripts are migration references;
they are not part of the Go runtime or Go build path.

## Source and license

Derived from [t8y2/dbx](https://github.com/t8y2/dbx), baseline
`977ca7bd382bff0bc300d453bd6f336580b5a56c`, plus the existing local workbench
changes. Original license, notices and contributor attribution are retained.
See [LICENSE](LICENSE). The preserved Rust snapshot is maintained separately in
[sugaryiiy/dbx-rust](https://github.com/sugaryiiy/dbx-rust).
