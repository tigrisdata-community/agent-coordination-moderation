package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tigrisdata/agent-coordination-moderation/internal/classify"
	"github.com/tigrisdata/agent-coordination-moderation/internal/store"
	"github.com/tigrisdata/agent-coordination-moderation/internal/webhook"
)

// deduper tracks seen ETags to handle Tigris at-least-once delivery.
type deduper struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func newDeduper() *deduper {
	return &deduper{seen: make(map[string]time.Time)}
}

// check returns true if the ETag has not been seen before.
func (d *deduper) check(etag string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.seen[etag]; ok {
		return false
	}
	d.seen[etag] = time.Now()
	return true
}

// cleanup removes entries older than maxAge.
func (d *deduper) cleanup(maxAge time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cutoff := time.Now().Add(-maxAge)
	for k, t := range d.seen {
		if t.Before(cutoff) {
			delete(d.seen, k)
		}
	}
}

func main() {
	bucketName := os.Getenv("BUCKET_NAME")
	if bucketName == "" {
		slog.Error("BUCKET_NAME is required")
		os.Exit(1)
	}

	webhookSecret := os.Getenv("WEBHOOK_SECRET")
	if webhookSecret == "" {
		slog.Error("WEBHOOK_SECRET is required")
		os.Exit(1)
	}

	reviewURL := os.Getenv("REVIEW_WEBHOOK_URL")
	port := os.Getenv("PORT_ROUTER")
	if port == "" {
		port = "8081"
	}

	ctx := context.Background()

	st, err := store.New(ctx, bucketName)
	if err != nil {
		slog.Error("failed to create store", "error", err)
		os.Exit(1)
	}

	dedup := newDeduper()

	// Periodic cleanup of old ETag entries.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			dedup.cleanup(1 * time.Hour)
		}
	}()

	mux := http.NewServeMux()
	handler := handleWebhook(st, dedup, reviewURL)
	mux.Handle("POST /webhook", webhook.VerifyBearer(webhookSecret, handler))

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	slog.Info("router agent listening", "port", port)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
	slog.Info("shutdown complete")
}

func handleWebhook(st *store.Store, dedup *deduper, reviewURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var payload webhook.NotificationPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			slog.Error("failed to decode notification", "error", err)
			http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
			return
		}

		for _, event := range payload.Events {
			if err := processEvent(r.Context(), st, dedup, reviewURL, event); err != nil {
				slog.Error("failed to process event",
					"key", event.Object.Key,
					"error", err,
				)
				// Return 500 so Tigris retries delivery.
				http.Error(w, `{"error":"processing failed"}`, http.StatusInternalServerError)
				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func processEvent(ctx context.Context, st *store.Store, dedup *deduper, reviewURL string, event webhook.NotificationEvent) error {
	// Only handle object creation in the pending/ prefix.
	if event.EventName != "OBJECT_CREATED_PUT" {
		return nil
	}
	if !strings.HasPrefix(event.Object.Key, "pending/") {
		return nil
	}

	// Deduplicate by ETag.
	if !dedup.check(event.Object.ETag) {
		slog.Debug("skipping duplicate event", "key", event.Object.Key, "etag", event.Object.ETag)
		return nil
	}

	// Read the classification result.
	var result classify.ClassificationResult
	_, err := st.Read(ctx, event.Object.Key, &result)
	if err != nil {
		// If the object is gone, it was already processed by a prior delivery.
		if strings.Contains(err.Error(), "NoSuchKey") {
			slog.Info("object already processed", "key", event.Object.Key)
			return nil
		}
		return err
	}

	// Determine destination prefix.
	var destPrefix string
	switch {
	case result.Violation && result.Confidence > 0.85:
		destPrefix = "flagged/"
	case result.Violation:
		destPrefix = "needs-review/"
	default:
		destPrefix = "approved/"
	}

	// Build destination key by replacing the pending/ prefix.
	filename := strings.TrimPrefix(event.Object.Key, "pending/")
	destKey := destPrefix + filename

	slog.Info("routing content",
		"id", result.ID,
		"from", event.Object.Key,
		"to", destKey,
		"violation", result.Violation,
		"confidence", result.Confidence,
	)

	if err := st.Move(ctx, event.Object.Key, destKey); err != nil {
		// If source is gone, another delivery already moved it.
		if strings.Contains(err.Error(), "NoSuchKey") {
			slog.Info("object already moved", "key", event.Object.Key)
			return nil
		}
		return err
	}

	// For flagged content, notify the review webhook.
	if destPrefix == "flagged/" && reviewURL != "" {
		if err := notifyReview(ctx, reviewURL, &result); err != nil {
			// Log but don't fail -- the object is already moved.
			slog.Error("review webhook notification failed",
				"id", result.ID,
				"url", reviewURL,
				"error", err,
			)
		}
	}

	return nil
}

func notifyReview(ctx context.Context, reviewURL string, result *classify.ClassificationResult) error {
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reviewURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return &reviewError{statusCode: resp.StatusCode}
	}
	return nil
}

type reviewError struct {
	statusCode int
}

func (e *reviewError) Error() string {
	return "review webhook returned " + http.StatusText(e.statusCode)
}
