package main

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestFormatDuration(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"0":         "",
		"90":        "1:30",
		"3600":      "1:00:00",
		"5461":      "1:31:01",
		"1:30":      "1:30",
		"01:30":     "1:30",
		"1:02:03":   "1:02:03",
		"00:45:00":  "45:00",
		" 120 ":     "2:00",
		"not known": "not known",
		"12:ab":     "12:ab",
	}

	for raw, want := range cases {
		if got := formatDuration(raw); got != want {
			t.Errorf("formatDuration(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestFormatSize(t *testing.T) {
	cases := map[string]string{
		"":           "",
		"0":          "",
		"-5":         "",
		"not a size": "",
		"1048576":    "1.0 MB",
		"5242880":    "5.0 MB",
		"52428800":   "50 MB",
	}

	for raw, want := range cases {
		if got := formatSize(raw); got != want {
			t.Errorf("formatSize(%q) = %q, want %q", raw, got, want)
		}
	}
}

// A feed without itunes:duration still has a download size, which is the only
// hint of length it gives.
func TestItemLengthFallsBackToSize(t *testing.T) {
	withDuration := previewFeedItem{Duration: "3600"}
	withDuration.Enclure.Length = "52428800"
	if got := itemLength(withDuration); got != "1:00:00" {
		t.Errorf("duration should win, got %q", got)
	}

	sizeOnly := previewFeedItem{}
	sizeOnly.Enclure.Length = "52428800"
	if got := itemLength(sizeOnly); got != "50 MB" {
		t.Errorf("got %q, want 50 MB", got)
	}

	if got := itemLength(previewFeedItem{}); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestItemSubtitleStripsMarkupAndTruncates(t *testing.T) {
	item := previewFeedItem{Descr: "<p>Эхо   <b>Москвы</b></p>\n<br/> про всё"}
	if got := itemSubtitle(item); got != "Эхо Москвы про всё" {
		t.Errorf("got %q", got)
	}

	// itunes:subtitle wins when both are present.
	item.Subtitle = "короткое описание"
	if got := itemSubtitle(item); got != "короткое описание" {
		t.Errorf("got %q", got)
	}

	long := previewFeedItem{Descr: ""}
	for range 100 {
		long.Descr += "Шульман "
	}
	got := itemSubtitle(long)
	if len(got) > maxSubtitleLength+len("…") {
		t.Errorf("subtitle is %d bytes, want it capped", len(got))
	}
	if got[len(got)-len("…"):] != "…" {
		t.Errorf("truncated subtitle should end with an ellipsis, got %q", got[len(got)-10:])
	}
}

func rewriteForTest(t *testing.T, raw string) string {
	t.Helper()
	out, _, err := filterFeed([]byte(raw), feedRewrite{
		Keep:       func(Item) bool { return true },
		Link:       "https://pf.example/filter?id=1",
		FilterLink: "https://pf.example/?id=1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// An HTML description gets the link as HTML, and the iTunes copies of the title
// and the description are rewritten under the prefix the feed declared.
func TestFilterFeedRewritesHTMLDescription(t *testing.T) {
	out := rewriteForTest(t, `<rss xmlns:it="http://www.itunes.com/DTDs/Podcast-1.0.dtd"><channel>
<title>Эхо</title><it:title>Эхо</it:title>
<description><![CDATA[<p>Про всё & сразу</p>]]></description>
<it:summary>Про всё</it:summary>
</channel></rss>`)

	var feed struct {
		Channel struct {
			Title       []string `xml:"title"`
			Description string   `xml:"description"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal([]byte(out), &feed); err != nil {
		t.Fatalf("%v in %s", err, out)
	}

	if want := []string{"Эхо (filtered)", "Эхо (filtered)"}; !equal(feed.Channel.Title, want) {
		t.Errorf("titles = %q, want %q", feed.Channel.Title, want)
	}
	want := `<p>Filter: <a href="https://pf.example/?id=1">https://pf.example/?id=1</a></p><p>Про всё & сразу</p>`
	if feed.Channel.Description != want {
		t.Errorf("description = %q, want %q", feed.Channel.Description, want)
	}
	if !strings.Contains(out, "<it:summary>Filter: https://pf.example/?id=1\n\nПро всё</it:summary>") {
		t.Errorf("itunes:summary not rewritten in %s", out)
	}
}

// A feed with no description, or an empty one, still gets the link.
func TestFilterFeedAddsMissingDescription(t *testing.T) {
	for _, raw := range []string{
		`<rss><channel><title>Эхо</title></channel></rss>`,
		`<rss><channel><title>Эхо</title><description/></channel></rss>`,
	} {
		out := rewriteForTest(t, raw)
		if strings.Count(out, "<description") != 1 ||
			!strings.Contains(out, "<description>Filter: https://pf.example/?id=1</description>") {
			t.Errorf("%s became %s, want a single description with the link", raw, out)
		}
	}
}

func TestFilterFeedRejectsNonRSS(t *testing.T) {
	_, _, err := filterFeed([]byte(`<feed xmlns="http://www.w3.org/2005/Atom"></feed>`), feedRewrite{})
	if err == nil {
		t.Fatal("an Atom feed should be refused")
	}
}
