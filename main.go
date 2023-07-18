package main

import (
	"encoding/xml"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
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

func GetPodcasts(podcastURL string) (*Podcast, error) {
	resp, err := http.Get(podcastURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var podcast *Podcast
	if err := xml.NewDecoder(resp.Body).Decode(&podcast); err != nil {
		return nil, err
	}

	return podcast, nil
}

func FilterPodcasts(items []Item, regex *regexp.Regexp, negative bool) []Item {
	filteredItems := make([]Item, 0)
	for _, item := range items {
		isMatch := regex.MatchString(item.Title)
		if isMatch && !negative || !isMatch && negative {
			filteredItems = append(filteredItems, item)
		}
	}
	return filteredItems
}

func logAndWriteErrorf(w http.ResponseWriter, code int, format string, a ...any) {
	err := fmt.Errorf(format, a...)
	log.Println(err)
	http.Error(w, err.Error(), code)
}

func filterHandler(w http.ResponseWriter, r *http.Request) {
	podcastURL := r.URL.Query().Get("feed")
	title := r.URL.Query().Get("title")
	res := r.URL.Query()["re"]
	negs := r.URL.Query()["neg"]
	if len(negs) > 0 && len(res) != len(negs) {
		logAndWriteErrorf(w, http.StatusServiceUnavailable, "Number of negs should be equal to REs, if not empty.")
		return
	}

	var feed *Podcast
	feed, err := GetPodcasts(podcastURL)
	if err != nil {
		logAndWriteErrorf(w, http.StatusUnprocessableEntity, "can't fetch origin podcast feed: %w", err)
		return
	}

	base_url := os.Getenv("BASE_URL")
	feed.Channel.Link = base_url + r.URL.String()

	feed.Channel.Descr = fmt.Sprintf("%s \n %s", r.URL, feed.Channel.Descr)

	if title == "" {
		title = fmt.Sprintf("%s (filtered)", feed.Channel.Title)
	}
	log.Printf("Change feed title: %s -> %s < %s >\n", feed.Channel.Title, title, r.URL)
	feed.Channel.Title = title

	items, err := feed.Channel.Items, nil

	log.Printf("items before: %d\n", len(items))

	for i, re := range res {
		negative := false
		if len(negs) > 0 {
			negative = negs[i] == "true"
		}

		log.Printf("filter: %s neg: %t re: %s\n", feed.Channel.Title, negative, re)

		regex, err := regexp.Compile(re)
		if err != nil {
			logAndWriteErrorf(w, http.StatusUnprocessableEntity, "can't filter feed: %w", err)
			return
		}

		items = FilterPodcasts(items, regex, negative)
	}

	log.Printf("items after: %d\n", len(items))

	feed.Channel.Items = items

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(feed); err != nil {
		err = fmt.Errorf("Failed to encode filtered feed: %w", err)
		log.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func main() {
	http.HandleFunc("/filter", filterHandler)

	log.Println("Starting server on port 8080...")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
