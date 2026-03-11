package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"resource-list/internal/store"
)

type Service struct {
	log         *log.Logger
	store       *store.Store
	readyFn     func() bool
	maxPageSize int
}

func New(logger *log.Logger, s *store.Store, readyFn func() bool, maxPageSize int) *Service {
	return &Service{
		log:         logger,
		store:       s,
		readyFn:     readyFn,
		maxPageSize: maxPageSize,
	}
}

func (s *Service) Run(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleHTMLText)
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/readyz", s.handleReady)
	mux.HandleFunc("/resources.txt", s.handleText)
	mux.HandleFunc("/snapshot.json", s.handleSnapshot)

	h := withNoCache(withAccessLog(s.log, mux))
	server := &http.Server{Addr: addr, Handler: h}

	errCh := make(chan error, 1)
	go func() {
		s.log.Printf("http server listening on %s", addr)
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func (s *Service) handleHTMLText(w http.ResponseWriter, r *http.Request) {
	s.writeLines(w, r, "text/html; charset=utf-8")
}

func (s *Service) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Service) handleReady(w http.ResponseWriter, _ *http.Request) {
	if !s.readyFn() {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready\n"))
}

func (s *Service) handleText(w http.ResponseWriter, r *http.Request) {
	s.writeLines(w, r, "text/plain; charset=utf-8")
}

func (s *Service) writeLines(w http.ResponseWriter, r *http.Request, contentType string) {
	f := s.parseFilters(r)
	recs := s.store.Snapshot(f)

	w.Header().Set("Content-Type", contentType)
	for _, rec := range recs {
		_, _ = w.Write([]byte(rec.Line()))
		_, _ = w.Write([]byte("\n"))
	}
}

func (s *Service) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	f := s.parseFilters(r)
	recs := s.store.Snapshot(f)
	lines := make([]string, 0, len(recs))
	for _, rec := range recs {
		lines = append(lines, rec.Line())
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(lines)
}

func (s *Service) parseFilters(r *http.Request) store.Filters {
	q := r.URL.Query()
	limit := atoiDefault(q.Get("limit"), s.maxPageSize)
	if limit > s.maxPageSize {
		limit = s.maxPageSize
	}
	if limit < 0 {
		limit = 0
	}
	offset := atoiDefault(q.Get("offset"), 0)
	if offset < 0 {
		offset = 0
	}

	return store.Filters{
		Namespace: strings.TrimSpace(q.Get("namespace")),
		Group:     strings.TrimSpace(q.Get("group")),
		Version:   strings.TrimSpace(q.Get("version")),
		Kind:      strings.TrimSpace(q.Get("kind")),
		Limit:     limit,
		Offset:    offset,
	}
}

func atoiDefault(raw string, fallback int) int {
	if strings.TrimSpace(raw) == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}

func withAccessLog(logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		logger.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(started))
	})
}

func withNoCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		w.Header().Set("Pragma", "no-cache")
		next.ServeHTTP(w, r)
	})
}
