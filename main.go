package main

import (
	"context"
	"embed"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
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

	feed, err := GetPodcasts(filter.Feed)
	if err != nil {
		logAndWriteErrorf(w, http.StatusUnprocessableEntity, "can't fetch origin podcast feed: %w", err)
		return
	}

	feed.Channel.Link = s.baseURL + r.URL.String()
	feed.Channel.Descr = fmt.Sprintf("%s \n %s", r.URL, feed.Channel.Descr)

	title := filter.Title
	if title == "" {
		title = fmt.Sprintf("%s (filtered)", feed.Channel.Title)
	}
	log.Printf("Change feed title: %s -> %s < %s >\n", feed.Channel.Title, title, r.URL)
	feed.Channel.Title = title

	log.Printf("items before: %d\n", len(feed.Channel.Items))
	for _, rule := range filter.Rules {
		log.Printf("filter: %s neg: %t re: %s\n", title, rule.Neg, rule.Re)
	}
	feed.Channel.Items = filter.Apply(feed.Channel.Items, regexes)
	log.Printf("items after: %d\n", len(feed.Channel.Items))

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(feed); err != nil {
		err = fmt.Errorf("failed to encode filtered feed: %w", err)
		log.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
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
	store, err := OpenStore(env("DB_PATH", "podcast-filter.db"))
	if err != nil {
		log.Fatalf("can't open the link store: %v", err)
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
		log.Fatal(err)
	}
	<-idle
}
