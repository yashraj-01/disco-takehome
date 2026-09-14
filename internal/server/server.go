// Package server exposes the pipeline over HTTP for "disco serve".
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/yashraj/disco/internal/llm"
	"github.com/yashraj/disco/internal/logging"
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
			// The browser gets this too, but only the person holding the tab
			// sees it there. A server that 500s silently in its own terminal
			// is the reason "it just spun forever" bug reports exist.
			// The hash, not the text: pipeline.Run already logged the text on
			// its "run started" line, and this must key to that same line.
			logging.L().Error("run failed", "brief", llm.ShortHash(brief), "err", err)
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.Header().Set("content-type", "application/json")
		if err := json.NewEncoder(w).Encode(campaign); err != nil {
			logging.L().Warn("writing response", "err", err)
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

// Listen exposes listen (see below) to other subcommands — namely
// "disco metrics" (internal/dashboard) — so the bind-with-fallback
// behaviour lives in exactly one place instead of being duplicated.
func Listen(addr string) (net.Listener, bool, error) {
	return listen(addr)
}

// listen binds addr, retrying once against an OS-assigned port (host with
// port 0) if the requested address is already in use. Any other bind error
// (a malformed address, a permissions error on a low port, ...) is returned
// as-is. The bool result reports whether the fallback was used.
func listen(addr string) (net.Listener, bool, error) {
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		return ln, false, nil
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		return nil, false, err
	}

	host, _, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		// addr didn't split as host:port; report the original bind error.
		return nil, false, err
	}
	fallback, ferr := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if ferr != nil {
		return nil, false, ferr
	}
	return fallback, true, nil
}

// Run serves until the process is stopped.
func Run(addr string, o pipeline.Options) error {
	ln, fellBack, err := listen(addr)
	if err != nil {
		return err
	}
	defer ln.Close()

	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		port = ln.Addr().String()
	}
	if fellBack {
		fmt.Printf("disco: address %s already in use; listening on http://localhost:%s instead\n", addr, port)
	} else {
		fmt.Printf("disco listening on http://localhost:%s\n", port)
	}

	srv := &http.Server{
		Handler:           New(o),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return srv.Serve(ln)
}
