package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Podcast struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	Channel Channel  `xml:"channel"`
}

type Channel struct {
	XMLName       xml.Name `xml:"channel"`
	Title         string   `xml:"title"`
	Link          string   `xml:"link"`
	Descr         string   `xml:"description"`
	PubDate       string   `xml:"pubDate"`
	LastBuildDate string   `xml:"lastBuildDate"`
	Items         []Item   `xml:"item"`
}

type Item struct {
	XMLName     xml.Name  `xml:"item"`
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	Enclure     Enclosure `xml:"enclosure"`
	Guid        string    `xml:"guid"`
	PubDate     string    `xml:"pubDate"`
	Author      string    `xml:"author"`
}

type Enclosure struct {
	XMLName xml.Name `xml:"enclosure"`
	Url     string   `xml:"url,attr"`
	Type    string   `xml:"type,attr"`
	Length  string   `xml:"length,attr"`
}

// previewFeed is a second, wider view of the same XML, used only by the
// builder. It is kept separate from Podcast on purpose: Podcast is re-encoded
// into the feed we serve, so a field added there would also show up in the
// output, while the builder just wants to read extra details.
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
// whichever view the caller needs.
func fetchFeed(podcastURL string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, podcastURL, nil)
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

func GetPodcasts(podcastURL string) (*Podcast, error) {
	raw, err := fetchFeed(podcastURL)
	if err != nil {
		return nil, err
	}

	var podcast *Podcast
	if err := xml.Unmarshal(raw, &podcast); err != nil {
		return nil, err
	}
	return podcast, nil
}

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

func (c *feedCache) Get(podcastURL string) (*previewFeed, error) {
	c.mu.Lock()
	entry, ok := c.entries[podcastURL]
	c.mu.Unlock()

	if ok && time.Since(entry.fetched) < feedCacheTTL {
		return entry.feed, nil
	}

	raw, err := fetchFeed(podcastURL)
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
	if raw == "" {
		return ""
	}

	total := 0
	for _, part := range strings.Split(raw, ":") {
		value, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || value < 0 {
			return raw // Not a shape we know; show it as the feed wrote it.
		}
		total = total*60 + value
	}

	if total <= 0 {
		return ""
	}
	if hours := total / 3600; hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, (total/60)%60, total%60)
	}
	return fmt.Sprintf("%d:%02d", total/60, total%60)
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
