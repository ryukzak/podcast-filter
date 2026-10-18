package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

//go:embed web
var webFS embed.FS

type server struct {
	store   *Store
	cache   *feedCache
	baseURL string
}

func logAndWriteErrorf(w http.ResponseWriter, code int, format string, a ...any) {
	err := fmt.Errorf(format, a...)
	log.Println(err)
	http.Error(w, err.Error(), code)
}

// filterHandler serves the filtered feed. The filter comes either inline in the
// query (feed/title/re/neg) or from the store when an id is given.
func (s *server) filterHandler(w http.ResponseWriter, r *http.Request) {
	filter, err := s.resolveFilter(r)
	if err != nil {
		code := http.StatusUnprocessableEntity
		if errors.Is(err, ErrLinkNotFound) {
			code = http.StatusNotFound
		}
		logAndWriteErrorf(w, code, "can't resolve filter: %w", err)
		return
	}

	regexes, err := filter.Compile()
	if err != nil {
		logAndWriteErrorf(w, http.StatusUnprocessableEntity, "can't filter feed: %w", err)
		return
	}

	raw, err := fetchFeed(r.Context(), filter.Feed)
	if err != nil {
		logAndWriteErrorf(w, http.StatusUnprocessableEntity, "can't fetch origin podcast feed: %w", err)
		return
	}

	// The same query on / opens the filter in the builder.
	origin := s.publicOrigin(r)
	feed, stats, err := filterFeed(raw, feedRewrite{
		Keep:       func(item Item) bool { return filter.accepts(item, regexes) },
		Title:      filter.Title,
		Link:       origin + r.URL.String(),
		FilterLink: origin + "/?" + r.URL.RawQuery,
	})
	if err != nil {
		logAndWriteErrorf(w, http.StatusUnprocessableEntity, "can't filter feed: %w", err)
		return
	}

	log.Printf("Change feed title: %s -> %s < %s >\n", stats.OriginTitle, stats.Title, r.URL)
	for _, rule := range filter.Rules {
		log.Printf("filter: %s neg: %t re: %s\n", stats.Title, rule.Neg, rule.Re)
	}
	log.Printf("items: %d -> %d\n", stats.Total, stats.Kept)

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	if _, err := w.Write(feed); err != nil {
		log.Printf("can't write filtered feed: %v\n", err)
	}
}

// publicOrigin is where podcast clients reach the service: BASE_URL when it is
// set, otherwise the host the request was sent to.
func (s *server) publicOrigin(r *http.Request) string {
	if s.baseURL != "" {
		base := strings.TrimSuffix(s.baseURL, "/")
		if !strings.Contains(base, "://") {
			base = "http://" + base // as in the README: BASE_URL='localhost:8080'
		}
		return base
	}

	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *server) resolveFilter(r *http.Request) (Filter, error) {
	if id := r.URL.Query().Get("id"); id != "" {
		link, err := s.store.Get(id)
		if err != nil {
			return Filter{}, err
		}
		return link.Filter, nil
	}
	return FilterFromQuery(r.URL.Query())
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /filter", s.filterHandler)
	mux.HandleFunc("POST /api/preview", s.previewHandler)
	mux.HandleFunc("POST /api/links", s.createLinkHandler)
	mux.HandleFunc("GET /api/links/{id}", s.getLinkHandler)
	mux.HandleFunc("PUT /api/links/{id}", s.updateLinkHandler)
	mux.HandleFunc("GET /api/config", s.configHandler)

	web, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("can't prepare embedded web assets: %v", err)
	}
	mux.Handle("GET /", http.FileServerFS(web))

	return mux
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// run lives apart from main so that its deferred store.Close still runs when
// the server fails to start; log.Fatal would skip it.
func run() error {
	store, err := OpenStore(env("DB_PATH", "podcast-filter.db"))
	if err != nil {
		return fmt.Errorf("can't open the link store: %w", err)
	}
	defer func() {
		// A failure here means the bbolt file may not have been flushed.
		if err := store.Close(); err != nil {
			log.Printf("can't close the link store: %v\n", err)
		}
	}()

	s := &server{
		store:   store,
		cache:   newFeedCache(),
		baseURL: os.Getenv("BASE_URL"),
	}

	addr := ":" + env("PORT", "8080")
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// The bbolt file has to be closed cleanly, so wait for a signal instead of
	// letting the process be killed mid-write.
	idle := make(chan struct{})
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		<-stop

		log.Println("Shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("shutdown: %v\n", err)
		}
		close(idle)
	}()

	log.Printf("Starting server on %s...\n", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-idle
	return nil
}
