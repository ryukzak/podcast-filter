package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

const sampleRSS = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd">
  <channel>
    <title>Эхо</title>
    <link>https://example.com</link>
    <description>origin</description>
    <item>
      <title>Шульман про всё</title>
      <pubDate>Mon, 02 Jan 2006 15:04:05 +0000</pubDate>
      <itunes:duration>3600</itunes:duration>
      <itunes:subtitle>О самом важном</itunes:subtitle>
    </item>
    <item>
      <title>LIVE: Шульман</title>
      <pubDate>Tue, 03 Jan 2006 15:04:05 +0000</pubDate>
      <enclosure url="https://example.com/2.mp3" type="audio/mpeg" length="52428800"/>
    </item>
    <item><title>Кто-то другой</title><pubDate>Wed, 04 Jan 2006 15:04:05 +0000</pubDate></item>
  </channel>
</rss>`

type testEnv struct {
	server *server
	mux    http.Handler
	origin *httptest.Server
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, sampleRSS)
	}))
	t.Cleanup(origin.Close)

	store, err := OpenStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := &server{store: store, cache: newFeedCache()}
	return &testEnv{server: s, mux: s.routes(), origin: origin}
}

func (e *testEnv) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(raw))
	}

	req := httptest.NewRequest(method, path, reader)
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("can't decode %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestIndexIsServed(t *testing.T) {
	env := newTestEnv(t)
	rec := env.do(t, http.MethodGet, "/", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Podcast Filter") {
		t.Fatal("index.html does not look like the builder page")
	}
}

func TestPreviewMarksItems(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(t, http.MethodPost, "/api/preview", previewRequest{Filter: Filter{
		Feed:  env.origin.URL,
		Rules: []Rule{{Re: "(?i)live", Neg: true}, {Re: "(?i)шульман"}},
	}})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body)
	}

	resp := decode[previewResponse](t, rec)
	if resp.Total != 3 || resp.Kept != 1 {
		t.Fatalf("total = %d, kept = %d", resp.Total, resp.Kept)
	}
	if resp.FeedTitle != "Эхо" {
		t.Fatalf("feedTitle = %q", resp.FeedTitle)
	}

	// Every item is reported, passing or not, so the preview list keeps a
	// stable height while the filter is edited.
	if len(resp.Items) != 3 {
		t.Fatalf("items = %d, want all 3", len(resp.Items))
	}

	kept := resp.Items[0]
	if !kept.Passed || kept.RejectedBy != -1 {
		t.Fatalf("item 0 = %+v, want passing", kept)
	}
	if kept.Matches[0] || !kept.Matches[1] {
		t.Fatalf("item 0 matches = %v, want [false true]", kept.Matches)
	}

	live := resp.Items[1]
	if live.Passed || live.RejectedBy != 0 {
		t.Fatalf("item 1 = %+v, want dropped by rule 0", live)
	}

	// Nothing excluded this one; it simply never matched a keep rule, which is
	// reported with no culprit rather than by blaming one of them.
	other := resp.Items[2]
	if other.Passed || other.RejectedBy != -1 {
		t.Fatalf("item 2 = %+v, want dropped with no culprit", other)
	}

	// The length column prefers itunes:duration and falls back to the
	// download size; an item with neither is simply blank.
	if kept.Length != "1:00:00" {
		t.Fatalf("item 0 length = %q, want 1:00:00", kept.Length)
	}
	if kept.Subtitle != "О самом важном" {
		t.Fatalf("item 0 subtitle = %q", kept.Subtitle)
	}
	if live.Length != "50 MB" {
		t.Fatalf("item 1 length = %q, want 50 MB", live.Length)
	}
	if other.Length != "" {
		t.Fatalf("item 2 length = %q, want empty", other.Length)
	}
}

// The feed we serve must not grow the extra elements the preview reads.
func TestFilteredFeedDoesNotLeakPreviewFields(t *testing.T) {
	env := newTestEnv(t)

	filter := Filter{Feed: env.origin.URL, Rules: []Rule{{Re: "(?i)шульман"}}}
	rec := env.do(t, http.MethodGet, "/filter?"+filter.Query().Encode(), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}

	for _, leaked := range []string{"<duration>", "<subtitle>", "О самом важном"} {
		if strings.Contains(rec.Body.String(), leaked) {
			t.Fatalf("served feed contains %q", leaked)
		}
	}
}

// A half-typed pattern must not break the preview: the broken rule is reported
// and skipped, and the rest keeps working.
func TestPreviewSkipsBrokenRule(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(t, http.MethodPost, "/api/preview", previewRequest{Filter: Filter{
		Feed:  env.origin.URL,
		Rules: []Rule{{Re: "(?i)(unclosed"}, {Re: "(?i)шульман"}},
	}})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body)
	}

	resp := decode[previewResponse](t, rec)
	if resp.RuleErrors[0] == "" {
		t.Fatal("rule 0 should be reported as broken")
	}
	if resp.RuleErrors[1] != "" {
		t.Fatalf("rule 1 should be fine, got %q", resp.RuleErrors[1])
	}
	if resp.Kept != 2 {
		t.Fatalf("kept = %d, want the 2 items matched by rule 1", resp.Kept)
	}
}

// Two keep rules are a union, and a drop rule still overrules them.
func TestPreviewOrsKeepRules(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(t, http.MethodPost, "/api/preview", previewRequest{Filter: Filter{
		Feed:  env.origin.URL,
		Rules: []Rule{{Re: "(?i)шульман"}, {Re: "(?i)другой"}},
	}})
	resp := decode[previewResponse](t, rec)
	if resp.Kept != 3 {
		t.Fatalf("kept = %d, want all 3 episodes", resp.Kept)
	}

	rec = env.do(t, http.MethodPost, "/api/preview", previewRequest{Filter: Filter{
		Feed: env.origin.URL,
		Rules: []Rule{
			{Re: "(?i)шульман"},
			{Re: "(?i)другой"},
			{Re: "(?i)live", Neg: true},
		},
	}})
	resp = decode[previewResponse](t, rec)
	if resp.Kept != 2 {
		t.Fatalf("kept = %d, want the drop rule to remove the LIVE episode", resp.Kept)
	}
	if excluded := resp.Items[1]; excluded.Passed || excluded.RejectedBy != 2 {
		t.Fatalf("item 1 = %+v, want dropped by rule 2", excluded)
	}
}

func TestPreviewRejectsBadFeedURL(t *testing.T) {
	env := newTestEnv(t)

	for _, feed := range []string{"", "   ", "file:///etc/passwd", "not a url"} {
		rec := env.do(t, http.MethodPost, "/api/preview", previewRequest{Filter: Filter{Feed: feed}})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("feed %q: code = %d, want 400", feed, rec.Code)
		}
	}
}

func TestPreviewUsesCache(t *testing.T) {
	hits := 0
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = io.WriteString(w, sampleRSS)
	}))
	defer origin.Close()

	env := newTestEnv(t)
	for range 3 {
		rec := env.do(t, http.MethodPost, "/api/preview", previewRequest{
			Filter: Filter{Feed: origin.URL, Rules: []Rule{{Re: "."}}},
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("code = %d, body = %s", rec.Code, rec.Body)
		}
	}

	if hits != 1 {
		t.Fatalf("origin was fetched %d times, want 1", hits)
	}
}

func TestLinkLifecycle(t *testing.T) {
	env := newTestEnv(t)
	filter := Filter{Feed: env.origin.URL, Title: "Эхо (часть)", Rules: []Rule{{Re: "(?i)шульман"}}}

	rec := env.do(t, http.MethodPost, "/api/links", linkRequest{Filter: filter, Password: "hunter2"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: code = %d, body = %s", rec.Code, rec.Body)
	}
	created := decode[linkResponse](t, rec)
	if !created.Mutable || created.Path != "/filter?id="+created.ID {
		t.Fatalf("created = %+v", created)
	}

	// Pasting the link back into the builder has to return the filter.
	rec = env.do(t, http.MethodGet, "/api/links/"+created.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: code = %d, body = %s", rec.Code, rec.Body)
	}
	loaded := decode[linkResponse](t, rec)
	if loaded.Filter == nil || loaded.Filter.Title != "Эхо (часть)" {
		t.Fatalf("loaded = %+v", loaded)
	}

	// And the feed itself has to be served from the stored filter.
	rec = env.do(t, http.MethodGet, created.Path, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("filter: code = %d, body = %s", rec.Code, rec.Body)
	}
	var feed Podcast
	if err := xml.Unmarshal(rec.Body.Bytes(), &feed); err != nil {
		t.Fatal(err)
	}
	if feed.Channel.Title != "Эхо (часть)" {
		t.Fatalf("title = %q", feed.Channel.Title)
	}
	if len(feed.Channel.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(feed.Channel.Items))
	}

	// Updating with the right password keeps the same URL.
	filter.Rules = append(filter.Rules, Rule{Re: "(?i)live", Neg: true})
	rec = env.do(t, http.MethodPut, "/api/links/"+created.ID, linkRequest{Filter: filter, Password: "hunter2"})
	if rec.Code != http.StatusOK {
		t.Fatalf("update: code = %d, body = %s", rec.Code, rec.Body)
	}

	rec = env.do(t, http.MethodGet, created.Path, nil)
	feed = Podcast{}
	if err := xml.Unmarshal(rec.Body.Bytes(), &feed); err != nil {
		t.Fatal(err)
	}
	if len(feed.Channel.Items) != 1 {
		t.Fatalf("items after update = %d, want 1", len(feed.Channel.Items))
	}
}

func TestUpdateLinkErrors(t *testing.T) {
	env := newTestEnv(t)
	filter := Filter{Feed: env.origin.URL, Rules: []Rule{{Re: "."}}}

	mutable := decode[linkResponse](t, env.do(t, http.MethodPost, "/api/links",
		linkRequest{Filter: filter, Password: "hunter2"}))
	immutable := decode[linkResponse](t, env.do(t, http.MethodPost, "/api/links",
		linkRequest{Filter: filter}))
	if immutable.Mutable {
		t.Fatal("a link created without a password must be immutable")
	}

	cases := []struct {
		name     string
		id       string
		password string
		want     int
	}{
		{"wrong password", mutable.ID, "nope", http.StatusForbidden},
		{"no password on a protected link", mutable.ID, "", http.StatusForbidden},
		{"immutable link", immutable.ID, "", http.StatusConflict},
		{"immutable link with a password", immutable.ID, "hunter2", http.StatusConflict},
		{"unknown link", "7a4f2b11-0000-4000-8000-000000000000", "hunter2", http.StatusNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.do(t, http.MethodPut, "/api/links/"+tc.id,
				linkRequest{Filter: filter, Password: tc.password})
			if rec.Code != tc.want {
				t.Fatalf("code = %d, want %d (body %s)", rec.Code, tc.want, rec.Body)
			}
		})
	}
}

func TestCreateLinkRejectsBrokenFilter(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(t, http.MethodPost, "/api/links", linkRequest{Filter: Filter{
		Feed:  env.origin.URL,
		Rules: []Rule{{Re: "(unclosed"}},
	}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestFilterHandlerUnknownID(t *testing.T) {
	env := newTestEnv(t)
	rec := env.do(t, http.MethodGet, "/filter?id=7a4f2b11-0000-4000-8000-000000000000", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}

// The inline query format documented before this change has to keep working.
func TestFilterHandlerInlineQuery(t *testing.T) {
	env := newTestEnv(t)

	filter := Filter{Feed: env.origin.URL, Rules: []Rule{{Re: "(?i)live", Neg: true}}}
	rec := env.do(t, http.MethodGet, "/filter?"+filter.Query().Encode(), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body)
	}

	var feed Podcast
	if err := xml.Unmarshal(rec.Body.Bytes(), &feed); err != nil {
		t.Fatal(err)
	}
	if len(feed.Channel.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(feed.Channel.Items))
	}
	if feed.Channel.Title != "Эхо (filtered)" {
		t.Fatalf("title = %q", feed.Channel.Title)
	}
}

func TestFilterHandlerMismatchedNeg(t *testing.T) {
	env := newTestEnv(t)
	q := url.Values{"feed": {env.origin.URL}, "re": {"a", "b"}, "neg": {"true"}}
	rec := env.do(t, http.MethodGet, "/filter?"+q.Encode(), nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code = %d, want 422", rec.Code)
	}
}

func TestConfigHandler(t *testing.T) {
	env := newTestEnv(t)
	resp := decode[configResponse](t, env.do(t, http.MethodGet, "/api/config", nil))
	if resp.MaxInlineURLLength != maxInlineURLLength {
		t.Fatalf("maxInlineUrlLength = %d", resp.MaxInlineURLLength)
	}
}

func TestPreviewTruncatesLongFeed(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`<rss version="2.0"><channel><title>Long</title>`)
	for i := range maxPreviewItems + 25 {
		fmt.Fprintf(&sb, "<item><title>episode %d</title></item>", i)
	}
	sb.WriteString(`</channel></rss>`)

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, sb.String())
	}))
	defer origin.Close()

	env := newTestEnv(t)
	rec := env.do(t, http.MethodPost, "/api/preview", previewRequest{
		Filter: Filter{Feed: origin.URL, Rules: []Rule{{Re: "episode 1$"}}},
	})

	resp := decode[previewResponse](t, rec)
	if !resp.Truncated || len(resp.Items) != maxPreviewItems {
		t.Fatalf("items = %d, truncated = %t", len(resp.Items), resp.Truncated)
	}
	// Counts cover the whole feed even though the list is cut short.
	if resp.Total != maxPreviewItems+25 || resp.Kept != 1 {
		t.Fatalf("total = %d, kept = %d", resp.Total, resp.Kept)
	}
}

// Item 0 is an hour long; the other two publish no duration and so pass.
func TestPreviewLengthBounds(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(t, http.MethodPost, "/api/preview", previewRequest{Filter: Filter{
		Feed:       env.origin.URL,
		MaxMinutes: 30,
	}})
	resp := decode[previewResponse](t, rec)
	if resp.Kept != 2 {
		t.Fatalf("kept = %d, want 2", resp.Kept)
	}
	if item := resp.Items[0]; item.Passed || item.LengthVerdict != lengthLong {
		t.Fatalf("item 0 = %+v, want dropped as too long", item)
	}

	// Inconsistent bounds are reported and ignored, like a broken pattern.
	rec = env.do(t, http.MethodPost, "/api/preview", previewRequest{Filter: Filter{
		Feed:       env.origin.URL,
		MinMinutes: 90,
		MaxMinutes: 30,
	}})
	resp = decode[previewResponse](t, rec)
	if resp.LengthError == "" || resp.Kept != 3 {
		t.Fatalf("lengthError = %q, kept = %d", resp.LengthError, resp.Kept)
	}
}

func TestFilterHandlerLengthBounds(t *testing.T) {
	env := newTestEnv(t)

	filter := Filter{Feed: env.origin.URL, MaxMinutes: 30}
	rec := env.do(t, http.MethodGet, "/filter?"+filter.Query().Encode(), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if strings.Contains(body, "Шульман про всё") || !strings.Contains(body, "Кто-то другой") {
		t.Fatalf("served feed = %s", body)
	}

	if rec := env.do(t, http.MethodPost, "/api/links", linkRequest{Filter: Filter{
		Feed: env.origin.URL, MinMinutes: 90, MaxMinutes: 30,
	}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("create with min > max: code = %d", rec.Code)
	}
}
