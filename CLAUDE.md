# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Run

```bash
# Build both services
go build ./...

# Run each agent (separate terminals)
go run ./cmd/ingest   # listens on PORT_INGEST (default 8080)
go run ./cmd/router   # listens on PORT_ROUTER (default 8081)

# Docker (builds both images in parallel)
docker buildx bake
```

No tests exist yet. No linter is configured.

## Architecture

Two independent HTTP services coordinate through Tigris object storage — no queue or database.

**Agent A (Ingest)** — `cmd/ingest/main.go`

- `POST /submit` accepts `{"id", "text", "media_url?"}`, responds `201` immediately
- Spawns a goroutine that calls `internal/classify` (Claude API) then writes the result to `pending/{id}.json` via `internal/store`
- Uses `sync.WaitGroup` for graceful shutdown of in-flight classifications

**Agent B (Router)** — `cmd/router/main.go`

- `POST /webhook` receives Tigris object notifications, guarded by `internal/webhook.VerifyBearer`
- Reads the classification from the bucket, routes the object by renaming it:
  - `flagged/` — violation detected, confidence > 0.85 (also POSTs to `REVIEW_WEBHOOK_URL`)
  - `needs-review/` — violation detected, confidence <= 0.85
  - `approved/` — no violation
- In-memory ETag deduper handles Tigris at-least-once delivery

**Shared packages** in `internal/`:

- `classify` — wraps `anthropic-sdk-go`, sends content to `claude-sonnet-4-0`, parses structured JSON response into `ClassificationResult`
- `store` — thin S3 helpers (`Write`, `Read`, `Move`, `Delete`) using AWS SDK v2 pointed at Tigris; `Move` uses the `X-Tigris-Rename: true` header for atomic renames
- `webhook` — Bearer token middleware and Tigris notification payload types (`NotificationPayload`, `NotificationEvent`, `NotificationObj`)

## Key Conventions

- Standard library `net/http` only — no routers or frameworks
- All configuration via environment variables (see README.md for the full table)
- Return HTTP 500 from the webhook handler on transient failures so Tigris retries delivery (up to 3x)
- Treat "object not found" errors during Move/Read as already-processed (return 200, not 500)
- The `store.New` constructor reads `AWS_ENDPOINT_URL` (default `https://fly.storage.tigris.dev`) for the Tigris S3 endpoint
