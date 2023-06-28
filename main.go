package main

import (
	"encoding/xml"
	"fmt"
	"log"
	"net/http"
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

func FilterPodcasts(items []Item, re string, negative bool) ([]Item, error) {
	regex, err := regexp.Compile(re)
	if err != nil {
		return nil, err
	}

	filteredItems := make([]Item, 0)
	for _, item := range items {
		isMatch := regex.MatchString(item.Title)
		if isMatch && !negative || !isMatch && negative {
			filteredItems = append(filteredItems, item)
		}
	}

	return filteredItems, nil
}

func filterHandler(w http.ResponseWriter, r *http.Request) {
	podcastURL := r.URL.Query().Get("feed")
	title := r.URL.Query().Get("title")
	res := r.URL.Query()["re"]
	negs := r.URL.Query()["neg"]
	if len(negs) > 0 && len(res) != len(negs) {
		http.Error(w, "Number of negs should be equal to REs, if not empty.", http.StatusBadRequest)
		return
	}

	var feed *Podcast
	feed, err := GetPodcasts(podcastURL)
	if err != nil {
		http.Error(w, fmt.Errorf("can't fetch podcast feed: %w", err).Error(), http.StatusInternalServerError)
		return
	}

	if title == "" {
		title = fmt.Sprintf("%s (filtered)", feed.Channel.Title)
	}
	log.Printf("%s -> %s\n", feed.Channel.Title, title)
	feed.Channel.Title = title

	items, err := feed.Channel.Items, nil

	for i, re := range res {
		negative := false
		if len(negs) > 0 {
			negative = negs[i] == "true"
		}

		log.Printf("%s neg: %t re: %s\n", feed.Channel.Title, negative, re)

		items, err = FilterPodcasts(items, re, negative)
		if err != nil {
			http.Error(w, fmt.Errorf("can't filter feed: %w", err).Error(), http.StatusBadRequest)
			return
		}
	}

	feed.Channel.Items = items

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(feed); err != nil {
		http.Error(w, fmt.Sprintf("Failed to encode filtered feed: %s", err.Error()), http.StatusInternalServerError)
		return
	}
}

func main() {
	http.HandleFunc("/filter", filterHandler)

	log.Println("Starting server on port 8080...")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
