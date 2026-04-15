# Agent Coordination: Content Moderation Pipeline

A two-agent content moderation pipeline demonstrating the writer/watcher
coordination pattern using Tigris object storage as the sole shared state.

```text
Client ─── POST /submit ──▶ [Agent A: Ingest]
                                   │
                            Claude classify
                                   │
                            Tigris: pending/{id}.json
                                   │
                            (object notification)
                                   │
                            [Agent B: Router]
                                   │
                      ┌────────────┼────────────┐
                      │            │            │
                   flagged/   needs-review/  approved/
                      │
               POST to REVIEW_WEBHOOK_URL
```

**Agent A (Ingest)** accepts content submissions, classifies them with Claude,
and writes results to `pending/{id}.json`. Returns immediately without waiting
for classification.

**Agent B (Router)** receives Tigris object notifications when new objects
land in `pending/`, reads the classification, and routes the object to
`flagged/`, `needs-review/`, or `approved/` based on the confidence score and
violation status. Flagged content triggers an outbound webhook to a
configurable review endpoint.

## Prerequisites

- Go 1.25+
- A [Tigris](https://www.tigrisdata.com) account with a bucket created
- The [Tigris CLI](https://www.tigrisdata.com/docs/cli/) (`tigris` or `t3`)
- An [Anthropic API key](https://console.anthropic.com/)

## Environment Variables

| Variable                   | Required     | Default                  | Description                                        |
| -------------------------- | ------------ | ------------------------ | -------------------------------------------------- |
| `TIGRIS_ACCESS_KEY_ID`     | Yes          |                          | Tigris access key (set as `AWS_ACCESS_KEY_ID`)     |
| `TIGRIS_SECRET_ACCESS_KEY` | Yes          |                          | Tigris secret key (set as `AWS_SECRET_ACCESS_KEY`) |
| `AWS_ENDPOINT_URL`         | No           | `https://t3.storage.dev` | Tigris S3 endpoint                                 |
| `BUCKET_NAME`              | Yes          |                          | Tigris bucket name                                 |
| `ANTHROPIC_API_KEY`        | Yes          |                          | Anthropic API key for Claude                       |
| `REVIEW_WEBHOOK_URL`       | No           |                          | URL to POST flagged content to                     |
| `WEBHOOK_SECRET`           | Yes (router) |                          | Bearer token for verifying Tigris notifications    |
| `PORT_INGEST`              | No           | `8080`                   | Port for the ingest agent                          |
| `PORT_ROUTER`              | No           | `8081`                   | Port for the router agent                          |

Tigris credentials map to the standard AWS SDK environment variables:

```bash
export AWS_ACCESS_KEY_ID="$TIGRIS_ACCESS_KEY_ID"
export AWS_SECRET_ACCESS_KEY="$TIGRIS_SECRET_ACCESS_KEY"
```

## Running

Start both agents in separate terminals:

```bash
# Terminal 1: Ingest Agent
export AWS_ACCESS_KEY_ID="tid_..."
export AWS_SECRET_ACCESS_KEY="tsec_..."
export AWS_REGION=auto
export AWS_ENDPOINT_URL_S3=https://t3.storage.dev
export BUCKET_NAME="moderation-pipeline"
export ANTHROPIC_API_KEY="sk-ant-..."
go run ./cmd/ingest

# Terminal 2: Router Agent
export AWS_ACCESS_KEY_ID="tid_..."
export AWS_SECRET_ACCESS_KEY="tsec_..."
export AWS_REGION=auto
export AWS_ENDPOINT_URL_S3=https://t3.storage.dev
export BUCKET_NAME="moderation-pipeline"
export WEBHOOK_SECRET="my-secret-token"
export REVIEW_WEBHOOK_URL="https://example.com/review"
go run ./cmd/router
```

## Setting Up Tigris Notifications

Wire the bucket to send object notifications to the router webhook. This
command tells Tigris to POST to the router whenever an object is created in
the `pending/` prefix:

```bash
tigris buckets set-notifications moderation-pipeline \
  --url https://your-router-host:8081/webhook \
  --token "$WEBHOOK_SECRET" \
  --filter 'WHERE `key` REGEXP "^pending/"'
```

Replace `moderation-pipeline` with your bucket name and
`https://your-router-host:8081` with the publicly-reachable URL of the router
agent.

## API

### POST /submit

Submit content for moderation.

**Request:**

```bash
curl -X POST http://localhost:8080/submit \
  -H "Content-Type: application/json" \
  -d '{
    "id": "post-123",
    "text": "Hello, this is a normal post.",
    "media_url": "https://example.com/image.jpg"
  }'
```

**Response (201 Created):**

```json
{
  "id": "post-123",
  "status": "pending"
}
```

The response returns immediately. Classification and routing happen
asynchronously. Check the Tigris bucket to see where the object ends up:

- `approved/post-123.json` — clean content
- `needs-review/post-123.json` — violation detected but low confidence (<= 0.85)
- `flagged/post-123.json` — violation detected with high confidence (> 0.85)

### Classification JSON

Each object in the bucket contains:

```json
{
  "id": "post-123",
  "text": "...",
  "media_url": "...",
  "categories": [
    { "name": "hate_speech", "flagged": false, "confidence": 0.95 },
    { "name": "spam", "flagged": false, "confidence": 0.9 },
    { "name": "nsfw", "flagged": false, "confidence": 0.92 },
    { "name": "harassment", "flagged": false, "confidence": 0.88 }
  ],
  "violation": false,
  "confidence": 0.95,
  "created_at": "2024-01-15T10:30:00Z"
}
```

## Design Decisions

- **No external queue or database.** Tigris object storage is the only shared
  state. Object notifications replace a message queue.
- **Async classification.** The ingest agent responds immediately and
  classifies in the background. This prevents client timeouts on slow Claude
  calls.
- **Idempotent processing.** Tigris delivers notifications at least once, so
  duplicates are expected. The router uses ETag-based deduplication and treats
  "object already moved" as a successful no-op.
- **Retry via HTTP 500.** The router returns 500 on transient failures, which
  causes Tigris to retry webhook delivery (up to 3 times).
- **Atomic moves.** Objects are moved between prefixes using the Tigris
  `X-Tigris-Rename` header for atomic renames, with a fallback to
  copy-then-delete.
