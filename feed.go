package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Item is the part of an episode the filter looks at.
type Item struct {
	Title    string `xml:"title"`
	Duration string `xml:"duration"` // itunes:duration, whatever the prefix is
}

// previewFeed is what the builder reads from a feed. The feed we serve is not
// decoded at all, see filterFeed.
type previewFeed struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Title string            `xml:"title"`
		Items []previewFeedItem `xml:"item"`
	} `xml:"channel"`
}

type previewFeedItem struct {
	Title    string `xml:"title"`
	Link     string `xml:"link"`
	Guid     string `xml:"guid"`
	PubDate  string `xml:"pubDate"`
	Author   string `xml:"author"`
	Duration string `xml:"duration"` // itunes:duration, whatever the prefix is
	Subtitle string `xml:"subtitle"` // itunes:subtitle
	Descr    string `xml:"description"`
	Enclure  struct {
		Url    string `xml:"url,attr"`
		Type   string `xml:"type,attr"`
		Length string `xml:"length,attr"`
	} `xml:"enclosure"`
}

const (
	fetchTimeout  = 30 * time.Second
	maxFeedSize   = 32 << 20 // 32 MiB is far above any sane podcast feed
	feedCacheTTL  = time.Minute
	userAgentName = "podcast-filter (+https://github.com/ryukzak/podcast-filter)"
)

var feedClient = &http.Client{Timeout: fetchTimeout}

// fetchFeed returns the raw XML, so that the same bytes can be decoded into
// whichever view the caller needs. The fetch stops when ctx is done, so a
// client that hangs up does not leave a slow origin request running.
func fetchFeed(ctx context.Context, podcastURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, podcastURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgentName)

	resp, err := feedClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("origin answered with %s", resp.Status)
	}

	return io.ReadAll(io.LimitReader(resp.Body, maxFeedSize))
}

const (
	itunesNS = "http://www.itunes.com/dtds/podcast-1.0.dtd"
	atomNS   = "http://www.w3.org/2005/Atom"
)

// feedRewrite is what filterFeed changes on the way from the origin feed to the
// feed we serve.
type feedRewrite struct {
	// Keep tells which episodes stay.
	Keep func(Item) bool
	// Title replaces the channel title; empty means "<origin title> (filtered)".
	Title string
	// Link replaces the channel link. It is the filtered feed itself.
	Link string
	// FilterLink goes in front of the channel description, so that the filter
	// can be found again from the podcast client.
	FilterLink string
}

// feedStats is what filterFeed did, for the log.
type feedStats struct {
	OriginTitle, Title string
	Total, Kept        int
}

// splice replaces raw[start:end] with text.
type splice struct {
	start, end int64
	text       string
}

// feedFilter is filterFeed's state while it walks the feed.
type feedFilter struct {
	feedRewrite
	dec       *xml.Decoder
	stats     feedStats
	described bool
}

// filterFeed copies the origin feed byte for byte, except for the episodes Keep
// drops and the few channel elements it rewrites. Decoding into a struct and
// encoding that back would lose whatever the struct does not name — the
// artwork, the author, the categories, half of every episode — and
// encoding/xml garbles namespace prefixes on top of that.
func filterFeed(raw []byte, rw feedRewrite) ([]byte, feedStats, error) {
	f := &feedFilter{feedRewrite: rw, dec: xml.NewDecoder(bytes.NewReader(raw))}
	var splices []splice

	depth := 0 // 1 inside <rss>, 2 inside <channel>
	for {
		start := f.dec.InputOffset()
		tok, err := f.dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, f.stats, err
		}

		switch tok := tok.(type) {
		case xml.StartElement:
			switch {
			case depth == 0 && tok.Name.Local != "rss":
				return nil, f.stats, fmt.Errorf("expected an RSS feed, got <%s>", tok.Name.Local)
			case depth == 0, depth == 1 && tok.Name.Local == "channel":
				depth++
			case depth == 1:
				if err := f.dec.Skip(); err != nil {
					return nil, f.stats, err
				}
			default:
				text, keep, err := f.channelChild(tok, raw[start:])
				if err != nil {
					return nil, f.stats, err
				}
				if !keep {
					splices = append(splices, splice{start, f.dec.InputOffset(), text})
				}
			}
		case xml.EndElement:
			depth--
			if depth == 1 && !f.described {
				// RSS requires a description, but not every feed has one.
				splices = append(splices, splice{start, start, element("description", f.description(""))})
			}
		}
	}

	var out bytes.Buffer
	out.Grow(len(raw))
	from := int64(0)
	for _, s := range splices {
		out.Write(raw[from:s.start])
		out.WriteString(s.text)
		from = s.end
	}
	out.Write(raw[from:])
	return out.Bytes(), f.stats, nil
}

// channelChild reads one child of <channel>, which tag starts with, and tells
// what replaces it unless keep is set. Empty text cuts the element out.
func (f *feedFilter) channelChild(start xml.StartElement, tag []byte) (text string, keep bool, err error) {
	var rewrite func(origin string) string

	name := start.Name
	switch {
	case name.Space == "" && name.Local == "item":
		return f.item(start)
	case name.Local == "title" && (name.Space == "" || isITunes(name)):
		rewrite = f.title
	case name.Space == "" && name.Local == "link":
		rewrite = func(string) string { return f.Link }
	case name.Space == "" && name.Local == "description":
		f.described = true
		rewrite = f.description
	case name.Local == "summary" && isITunes(name):
		rewrite = f.description
	// Both send podcast clients back to the origin feed, and a client that
	// follows one ends up subscribed to every episode again.
	case name.Space == atomNS && name.Local == "link", name.Local == "new-feed-url" && isITunes(name):
		return "", false, f.dec.Skip()
	default:
		return "", true, f.dec.Skip()
	}

	var origin string
	if err := f.dec.DecodeElement(&origin, &start); err != nil {
		return "", false, err
	}
	return element(qualifiedName(tag), rewrite(origin)), false, nil
}

func (f *feedFilter) item(start xml.StartElement) (text string, keep bool, err error) {
	var item Item
	if err := f.dec.DecodeElement(&item, &start); err != nil {
		return "", false, err
	}

	f.stats.Total++
	if !f.Keep(item) {
		return "", false, nil
	}
	f.stats.Kept++
	return "", true, nil
}

func (f *feedFilter) title(origin string) string {
	title := f.Title
	if title == "" {
		title = origin + " (filtered)"
	}
	f.stats.OriginTitle, f.stats.Title = origin, title
	return title
}

// description puts the link to the filter in front of the origin description,
// in the markup the description already uses, so that it reads right whether
// the client shows it as HTML or as text.
func (rw feedRewrite) description(origin string) string {
	if tagRe.MatchString(origin) {
		link := html.EscapeString(rw.FilterLink)
		return fmt.Sprintf(`<p>Filter: <a href="%s">%s</a></p>`, link, link) + origin
	}
	if strings.TrimSpace(origin) == "" {
		return "Filter: " + rw.FilterLink
	}
	return "Filter: " + rw.FilterLink + "\n\n" + origin
}

// isITunes tells whether name is in the iTunes namespace, which feeds spell in
// more than one case.
func isITunes(name xml.Name) bool {
	return strings.EqualFold(name.Space, itunesNS)
}

// qualifiedName reads the element name, prefix included, from the start tag at
// the head of tag. The decoder only reports the namespace a prefix stands for,
// and a rewritten element has to keep the prefix the feed declared.
func qualifiedName(tag []byte) string {
	name := tag[1:] // past the '<'
	if end := bytes.IndexAny(name, " \t\r\n/>"); end >= 0 {
		name = name[:end]
	}
	return string(name)
}

func element(qname, text string) string {
	return "<" + qname + ">" + textEscaper.Replace(text) + "</" + qname + ">"
}

// textEscaper escapes element text. Unlike xml.EscapeText it leaves line breaks
// alone, so a description stays readable in the raw feed.
var textEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// feedCache keeps recently fetched feeds for a short while. The builder
// re-evaluates the filter on every keystroke, and there is no reason to hit the
// origin feed again for each of them.
type feedCache struct {
	mu      sync.Mutex
	entries map[string]feedCacheEntry
}

type feedCacheEntry struct {
	feed    *previewFeed
	fetched time.Time
}

func newFeedCache() *feedCache {
	return &feedCache{entries: make(map[string]feedCacheEntry)}
}

func (c *feedCache) Get(ctx context.Context, podcastURL string) (*previewFeed, error) {
	c.mu.Lock()
	entry, ok := c.entries[podcastURL]
	c.mu.Unlock()

	if ok && time.Since(entry.fetched) < feedCacheTTL {
		return entry.feed, nil
	}

	raw, err := fetchFeed(ctx, podcastURL)
	if err != nil {
		return nil, err
	}

	feed := &previewFeed{}
	if err := xml.Unmarshal(raw, feed); err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.evictExpiredLocked()
	c.entries[podcastURL] = feedCacheEntry{feed: feed, fetched: time.Now()}
	c.mu.Unlock()

	return feed, nil
}

func (c *feedCache) evictExpiredLocked() {
	for key, entry := range c.entries {
		if time.Since(entry.fetched) >= feedCacheTTL {
			delete(c.entries, key)
		}
	}
}

// formatDuration normalises itunes:duration, which feeds write either as a
// number of seconds or as h:mm:ss / m:ss.
func formatDuration(raw string) string {
	raw = strings.TrimSpace(raw)
	total, ok := parseDuration(raw)
	if !ok {
		return raw // Not a shape we know; show it as the feed wrote it.
	}
	if total <= 0 {
		return ""
	}
	if hours := total / 3600; hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, (total/60)%60, total%60)
	}
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}

// parseDuration reads itunes:duration into seconds. ok is false for a value
// that is neither a number of seconds nor h:mm:ss / m:ss.
func parseDuration(raw string) (seconds int, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}

	for _, part := range strings.Split(raw, ":") {
		value, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || value < 0 {
			return 0, false
		}
		seconds = seconds*60 + value
	}
	return seconds, true
}

// formatSize renders the enclosure length, which is a byte count. It stands in
// for the duration when a feed does not publish one.
func formatSize(raw string) string {
	bytes, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || bytes <= 0 {
		return ""
	}

	mb := float64(bytes) / (1 << 20)
	if mb < 10 {
		return fmt.Sprintf("%.1f MB", mb)
	}
	return fmt.Sprintf("%.0f MB", mb)
}
