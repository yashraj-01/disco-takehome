// Package server exposes the pipeline over HTTP for "disco serve".
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/yashraj/disco/internal/pipeline"
	"github.com/yashraj/disco/web"
)

// New returns the handler: a single embedded page plus one JSON endpoint.
func New(o pipeline.Options) http.Handler {
	mux := http.NewServeMux()

	sub, err := fs.Sub(web.FS, ".")
	if err != nil {
		panic(err) // the embedded FS is compiled in; this cannot fail at runtime
	}
	// Registered as an unrestricted "/" rather than "GET /": a method-scoped
	// pattern here would conflict with "/api/run" below (also unrestricted,
	// since it does its own method check to produce 405 instead of 404) —
	// net/http's ServeMux refuses to register two patterns for overlapping
	// requests unless one is unambiguously more specific in both the method
	// and path dimensions, and "GET /" vs an all-methods "/api/run" is not.
	mux.Handle("/", http.FileServer(http.FS(sub)))

	mux.HandleFunc("/api/run", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeErr(w, http.StatusMethodNotAllowed, "POST a JSON body with a brief field")
			return
		}
		var req struct {
			Brief string `json:"brief"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "could not read request: "+err.Error())
			return
		}
		brief := strings.TrimSpace(req.Brief)
		if brief == "" {
			writeErr(w, http.StatusBadRequest, "describe the business in a sentence first")
			return
		}

		ctx, cancel := contextWithTimeout(r, 5*time.Minute)
		defer cancel()

		campaign, err := pipeline.Run(ctx, brief, o)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.Header().Set("content-type", "application/json")
		if err := json.NewEncoder(w).Encode(campaign); err != nil {
			log.Printf("serve: writing response: %v", err)
		}
	})

	return mux
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

// Run serves until the process is stopped.
func Run(addr string, o pipeline.Options) error {
	fmt.Printf("disco listening on http://localhost%s\n", addr)
	srv := &http.Server{
		Addr:              addr,
		Handler:           New(o),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return srv.ListenAndServe()
}
