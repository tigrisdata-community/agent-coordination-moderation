package webhook

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

// NotificationPayload is the top-level JSON sent by Tigris object notifications.
type NotificationPayload struct {
	Events []NotificationEvent `json:"events"`
}

// NotificationEvent represents a single object event from Tigris.
type NotificationEvent struct {
	EventVersion string          `json:"eventVersion"`
	EventSource  string          `json:"eventSource"`
	EventName    string          `json:"eventName"`
	EventTime    string          `json:"eventTime"`
	Bucket       string          `json:"bucket"`
	Object       NotificationObj `json:"object"`
}

// NotificationObj holds the object metadata within a notification event.
type NotificationObj struct {
	Key  string `json:"key"`
	Size int64  `json:"size"`
	ETag string `json:"eTag"`
}

// VerifyBearer returns middleware that checks the Authorization header for a
// Bearer token matching secret. Uses constant-time comparison.
func VerifyBearer(secret string, next http.Handler) http.Handler {
	expected := "Bearer " + secret
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") ||
			subtle.ConstantTimeCompare([]byte(auth), []byte(expected)) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
