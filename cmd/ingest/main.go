package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/tigrisdata/agent-coordination-moderation/internal/classify"
	"github.com/tigrisdata/agent-coordination-moderation/internal/store"
)

type submitRequest struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	MediaURL string `json:"media_url,omitempty"`
}

type submitResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func main() {
	bucketName := os.Getenv("BUCKET_NAME")
	if bucketName == "" {
		slog.Error("BUCKET_NAME is required")
		os.Exit(1)
	}

	port := os.Getenv("PORT_INGEST")
	if port == "" {
		port = "8080"
	}

	ctx := context.Background()

	claude := anthropic.NewClient()

	st, err := store.New(ctx, bucketName)
	if err != nil {
		slog.Error("failed to create store", "error", err)
		os.Exit(1)
	}

	var wg sync.WaitGroup
	mux := http.NewServeMux()
	mux.HandleFunc("POST /submit", handleSubmit(claude, st, &wg))

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	// Graceful shutdown: wait for in-flight classification goroutines.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	slog.Info("ingest agent listening", "port", port)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}

	slog.Info("waiting for in-flight classifications")
	wg.Wait()
	slog.Info("shutdown complete")
}

func handleSubmit(claude anthropic.Client, st *store.Store, wg *sync.WaitGroup) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req submitRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
			return
		}

		if req.ID == "" || req.Text == "" {
			http.Error(w, `{"error":"id and text are required"}`, http.StatusBadRequest)
			return
		}

		// Respond immediately -- classification happens in the background.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(submitResponse{
			ID:     req.ID,
			Status: "pending",
		})

		// Classify and write to Tigris in the background.
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()

			result, err := classify.Classify(ctx, claude, req.ID, req.Text, req.MediaURL)
			if err != nil {
				slog.Error("classification failed", "id", req.ID, "error", err)
				return
			}

			key := "pending/" + req.ID + ".json"
			if err := st.Write(ctx, key, result); err != nil {
				slog.Error("failed to write to tigris", "id", req.ID, "key", key, "error", err)
				return
			}

			slog.Info("classification complete", "id", req.ID, "violation", result.Violation, "confidence", result.Confidence)
		}()
	}
}
