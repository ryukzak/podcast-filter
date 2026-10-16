package main

import "testing"

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
