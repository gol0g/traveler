package web

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"time"

	"traveler/internal/config"
	"traveler/internal/provider"
)

//go:embed static
var staticFiles embed.FS

// Server represents the web server
type Server struct {
	config   *config.Config
	provider provider.Provider
	capital  float64
	universe string
	srv      *http.Server
}

// NewServer creates a new web server
func NewServer(cfg *config.Config, p provider.Provider, capital float64, universe string) *Server {
	return &Server{
		config:   cfg,
		provider: p,
		capital:  capital,
		universe: universe,
	}
}

// Start starts the web server on the specified port
func (s *Server) Start(port int) error {
	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("/api/scan", s.handleScan)
	mux.HandleFunc("/api/signals", s.handleSignals)
	mux.HandleFunc("/api/stock/", s.handleStock)
	mux.HandleFunc("/api/portfolio", s.handlePortfolio)
	mux.HandleFunc("/api/universes", s.handleUniverses)

	// Static files
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return fmt.Errorf("failed to create static file system: %w", err)
	}
	mux.Handle("/", http.FileServer(http.FS(staticFS)))

	s.srv = &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      corsMiddleware(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	log.Printf("Starting Traveler Web UI at http://localhost:%d", port)
	log.Printf("Press Ctrl+C to stop")

	return s.srv.ListenAndServe()
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv != nil {
		return s.srv.Shutdown(ctx)
	}
	return nil
}

// corsMiddleware adds CORS headers for local development
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}
