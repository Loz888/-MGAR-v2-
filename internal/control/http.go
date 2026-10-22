package control

import (
	"context"
	"net/http"
	"time"
)

func Handler() http.Handler {
	return HandlerWithReadiness(nil)
}

func HandlerWithReadiness(check func(context.Context) error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if check != nil {
			ctx, cancel := context.WithTimeout(r.Context(), time.Second)
			defer cancel()
			if check(ctx) != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"status":"unavailable","provider":"mock"}`))
				return
			}
		}
		_, _ = w.Write([]byte(`{"status":"ok","provider":"mock","runtime":"control_plane"}`))
	})
	return mux
}
