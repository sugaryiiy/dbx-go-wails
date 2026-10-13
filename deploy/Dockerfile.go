FROM node:22-bookworm AS ui
WORKDIR /src
RUN corepack enable && corepack prepare pnpm@10.27.0 --activate
COPY . .
# The imported frontend explicitly lists macOS tooling bindings. They are not
# executed during a Linux Vite build; force allows the shared lockfile install.
RUN pnpm --filter dbx... install --frozen-lockfile --force
ARG DBX_PUBLIC_BASE_PATH=""
ENV DBX_PUBLIC_BASE_PATH=${DBX_PUBLIC_BASE_PATH}
RUN pnpm build:ui

FROM golang:1.25-bookworm AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
COPY cmd ./cmd
RUN CGO_ENABLED=0 go build -mod=mod -trimpath -ldflags="-s -w" -o /dbx-web ./cmd/dbx-web

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10001 --home-dir /data dbx \
    && mkdir /data /app && chown dbx:dbx /data
WORKDIR /app
COPY --from=backend /dbx-web /app/dbx-web
COPY --from=ui /src/dist /app/dist
USER dbx
ENV DBX_DATA_DIR=/data DBX_BIND_ADDR=0.0.0.0 DBX_PORT=4224 DBX_STATIC_DIR=/app/dist
VOLUME ["/data"]
EXPOSE 4224
ENTRYPOINT ["/app/dbx-web"]
