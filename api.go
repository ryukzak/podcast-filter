package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// maxInlineURLLength is the length above which we stop recommending an
	// inline /filter URL. Some podcast clients silently truncate long URLs,
	// and a GUID link is the way around that.
	maxInlineURLLength = 512

	// maxPreviewItems bounds the preview payload for very long feeds.
	maxPreviewItems = 500

	// maxAPIBodySize bounds the JSON we are willing to read from a client.
	maxAPIBodySize = 1 << 20

	// maxSubtitleLength keeps episode descriptions from bloating the preview.
	maxSubtitleLength = 400
)

type previewRequest struct {
	Filter Filter `json:"filter"`
}

type previewItem struct {
	Title      string `json:"title"`
	Link       string `json:"link"`
	PubDate    string `json:"pubDate"`
	Length     string `json:"length"`
	Author     string `json:"author"`
	Subtitle   string `json:"subtitle"`
	Matches    []bool `json:"matches"`
	Passed     bool   `json:"passed"`
	RejectedBy int    `json:"rejectedBy"`
}

type previewResponse struct {
	FeedTitle  string        `json:"feedTitle"`
	Total      int           `json:"total"`
	Kept       int           `json:"kept"`
	Truncated  bool          `json:"truncated"`
	RuleErrors []string      `json:"ruleErrors"`
	Items      []previewItem `json:"items"`
}

type linkRequest struct {
	Filter   Filter `json:"filter"`
	Password string `json:"password"`
}

type linkResponse struct {
	ID        string  `json:"id"`
	Path      string  `json:"path"`
	Mutable   bool    `json:"mutable"`
	Filter    *Filter `json:"filter,omitempty"`
	CreatedAt string  `json:"createdAt,omitempty"`
	UpdatedAt string  `json:"updatedAt,omitempty"`
}

type configResponse struct {
	MaxInlineURLLength int `json:"maxInlineUrlLength"`
}

// previewHandler evaluates the filter against the origin feed and reports, per
// item, which rules matched and which rule dropped it. The evaluation runs on
// the server on purpose: Go's RE2 syntax (`(?i)` and friends) does not match
// what a browser regexp engine would do, and the builder has to show exactly
// what /filter will do.
func (s *server) previewHandler(w http.ResponseWriter, r *http.Request) {
	var req previewRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if err := validateFeedURL(req.Filter.Feed); err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}

	feed, err := s.cache.Get(req.Filter.Feed)
	if err != nil {
		log.Printf("preview: can't fetch %s: %v\n", req.Filter.Feed, err)
		writeJSONError(w, http.StatusUnprocessableEntity,
			fmt.Errorf("can't fetch origin podcast feed: %w", err))
		return
	}

	writeJSON(w, http.StatusOK, buildPreview(req.Filter, feed))
}

func buildPreview(filter Filter, feed *previewFeed) previewResponse {
	ruleErrors := filter.Validate()

	// Rules that do not compile are skipped instead of being fatal, so the
	// preview keeps working while a pattern is half-typed.
	regexes := make([]*regexp.Regexp, len(filter.Rules))
	for i, rule := range filter.Rules {
		if ruleErrors[i] != "" {
			continue
		}
		regexes[i] = regexp.MustCompile(rule.Re)
	}

	resp := previewResponse{
		FeedTitle:  feed.Channel.Title,
		Total:      len(feed.Channel.Items),
		RuleErrors: ruleErrors,
		Items:      make([]previewItem, 0, min(len(feed.Channel.Items), maxPreviewItems)),
	}

	for _, item := range feed.Channel.Items {
		preview := previewItem{
			Title:      item.Title,
			Link:       item.Link,
			PubDate:    item.PubDate,
			Length:     itemLength(item),
			Author:     item.Author,
			Subtitle:   itemSubtitle(item),
			Matches:    make([]bool, len(filter.Rules)),
			RejectedBy: -1,
		}

		// Mirrors Filter.accepts, but records why the episode is out: either a
		// single drop rule excluded it, or no keep rule took it.
		keepSeen, keepMatched := false, false
		for i, regex := range regexes {
			if regex == nil {
				continue
			}
			matched := regex.MatchString(item.Title)
			preview.Matches[i] = matched

			if filter.Rules[i].Neg {
				if matched && preview.RejectedBy == -1 {
					preview.RejectedBy = i
				}
				continue
			}
			keepSeen = true
			keepMatched = keepMatched || matched
		}
		preview.Passed = preview.RejectedBy == -1 && (!keepSeen || keepMatched)

		if preview.Passed {
			resp.Kept++
		}
		if len(resp.Items) < maxPreviewItems {
			resp.Items = append(resp.Items, preview)
		} else {
			resp.Truncated = true
		}
	}

	return resp
}

// itemLength prefers the published duration and falls back to the download
// size, which is the only hint of length some feeds give.
func itemLength(item previewFeedItem) string {
	if duration := formatDuration(item.Duration); duration != "" {
		return duration
	}
	return formatSize(item.Enclure.Length)
}

// itemSubtitle gives the detail view one line of context. Descriptions are
// often long HTML blobs, so tags are dropped and the text is cut short.
func itemSubtitle(item previewFeedItem) string {
	text := strings.TrimSpace(item.Subtitle)
	if text == "" {
		text = item.Descr
	}

	text = strings.Join(strings.Fields(tagRe.ReplaceAllString(text, " ")), " ")
	if len(text) > maxSubtitleLength {
		// Cut on a rune boundary, not in the middle of a Cyrillic letter.
		cut := maxSubtitleLength
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = strings.TrimSpace(text[:cut]) + "…"
	}
	return text
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

// createLinkHandler stores a filter behind a fresh GUID.
func (s *server) createLinkHandler(w http.ResponseWriter, r *http.Request) {
	var req linkRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if err := validateFilter(req.Filter); err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}

	link, err := s.store.Create(req.Filter, req.Password)
	if err != nil {
		log.Printf("can't create link: %v\n", err)
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}

	log.Printf("created link %s for %s (mutable: %t)\n", link.ID, link.Filter.Feed, link.Mutable())
	writeJSON(w, http.StatusCreated, newLinkResponse(link, false))
}

// getLinkHandler returns the stored filter so that the builder can load and
// debug an existing GUID link.
func (s *server) getLinkHandler(w http.ResponseWriter, r *http.Request) {
	link, err := s.store.Get(r.PathValue("id"))
	if err != nil {
		writeLinkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newLinkResponse(link, true))
}

// updateLinkHandler replaces the filter behind a GUID. It needs the password
// the link was created with; links created without one are immutable.
func (s *server) updateLinkHandler(w http.ResponseWriter, r *http.Request) {
	var req linkRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if err := validateFilter(req.Filter); err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}

	id := r.PathValue("id")
	link, err := s.store.Update(id, req.Filter, req.Password)
	if err != nil {
		writeLinkError(w, err)
		return
	}

	log.Printf("updated link %s for %s\n", link.ID, link.Filter.Feed)
	writeJSON(w, http.StatusOK, newLinkResponse(link, false))
}

func (s *server) configHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, configResponse{MaxInlineURLLength: maxInlineURLLength})
}

func newLinkResponse(link *Link, withFilter bool) linkResponse {
	resp := linkResponse{
		ID:        link.ID,
		Path:      "/filter?" + url.Values{"id": {link.ID}}.Encode(),
		Mutable:   link.Mutable(),
		CreatedAt: link.CreatedAt.Format("2006-01-02 15:04 UTC"),
		UpdatedAt: link.UpdatedAt.Format("2006-01-02 15:04 UTC"),
	}
	if withFilter {
		filter := link.Filter
		resp.Filter = &filter
	}
	return resp
}

func writeLinkError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrLinkNotFound):
		writeJSONError(w, http.StatusNotFound, err)
	case errors.Is(err, ErrImmutable):
		writeJSONError(w, http.StatusConflict, err)
	case errors.Is(err, ErrBadPassword):
		writeJSONError(w, http.StatusForbidden, err)
	default:
		log.Printf("link error: %v\n", err)
		writeJSONError(w, http.StatusInternalServerError, err)
	}
}

func validateFilter(filter Filter) error {
	if err := validateFeedURL(filter.Feed); err != nil {
		return err
	}
	for i, problem := range filter.Validate() {
		if problem != "" {
			return fmt.Errorf("filter #%d is invalid: %s", i+1, problem)
		}
	}
	return nil
}

func validateFeedURL(feedURL string) error {
	if strings.TrimSpace(feedURL) == "" {
		return errors.New("feed url is required")
	}
	parsed, err := url.Parse(feedURL)
	if err != nil {
		return fmt.Errorf("feed url is not a url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("feed url should be http or https")
	}
	if parsed.Host == "" {
		return errors.New("feed url has no host")
	}
	return nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxAPIBodySize)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Errorf("can't read request: %w", err))
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("can't write response: %v\n", err)
	}
}

func writeJSONError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
