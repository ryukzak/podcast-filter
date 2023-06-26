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

func FilterPodcasts(podcast *Podcast, re string, negative bool) ([]Item, error) {
	regex, err := regexp.Compile(re)
	if err != nil {
		return nil, err
	}

	filteredItems := make([]Item, 0)
	for _, item := range podcast.Channel.Items {
		isMatch := regex.MatchString(item.Title)
		if isMatch && !negative || !isMatch && negative {
			filteredItems = append(filteredItems, item)
		}
	}

	return filteredItems, nil
}

func filterHandler(w http.ResponseWriter, r *http.Request) {
	podcastURL := r.URL.Query().Get("feed")
	re := r.URL.Query().Get("re")
	negative := r.URL.Query().Get("neg") == "true"

	var feed *Podcast
	feed, err := GetPodcasts(podcastURL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	filteredItems, err := FilterPodcasts(feed, re, negative)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	feed.Channel.Title = fmt.Sprintf("%s (filtered)", feed.Channel.Title)
	feed.Channel.Items = filteredItems

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
