// RSS News Aggregator — prototype implementation.
//
// Current tier: single server + PostgreSQL (no cache, no distributed locks).
// See DESIGN.md for the full scale path:
//   Tier 1 — Redis cache in newsHandler         (10k → 1M users)
//   Tier 2 — Redis publisher lock in refreshAll (1M  → 10M users)
//   Tier 3 — Kafka pipeline, separate services  (10M → 100M users)
package main

import (
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	_ "github.com/lib/pq"
)

// ── Feed format structs ───────────────────────────────────────────────────────
// Supports RSS 2.0 (<channel>/<item>) and Atom 1.0 (<feed>/<entry>).
// parseFeed tries RSS first and falls back to Atom.

type RSS struct {
	Channel Channel `xml:"channel"`
}

type Channel struct {
	Items []Item `xml:"item"`
}

type Item struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
}

type AtomFeed struct {
	Entries []AtomEntry `xml:"entry"`
}

type AtomEntry struct {
	Title   string     `xml:"title"`
	Links   []AtomLink `xml:"link"`
	Summary string     `xml:"summary"`
	Content string     `xml:"content"`
	Updated string     `xml:"updated"`
}

type AtomLink struct {
	Rel  string `xml:"rel,attr"`
	HRef string `xml:"href,attr"`
}

func (e AtomEntry) link() string {
	for _, l := range e.Links {
		if l.Rel == "alternate" || l.Rel == "" {
			return l.HRef
		}
	}
	return ""
}

func (e AtomEntry) summary() string {
	if e.Summary != "" {
		return e.Summary
	}
	return e.Content
}

// ── API types ─────────────────────────────────────────────────────────────────

// Article is the JSON shape returned by /api/news.
type Article struct {
	Title       string `json:"title"`
	Link        string `json:"link"`
	Description string `json:"description"`
	PubDate     string `json:"pub_date"`
	Source      string `json:"source"`
	ParsedTime  int64  `json:"parsed_time"`
}

// Publisher mirrors the publishers table row.
type Publisher struct {
	ID      int
	Name    string
	FeedURL string
}

// ── Globals ───────────────────────────────────────────────────────────────────

var (
	db         *sql.DB
	httpClient = &http.Client{Timeout: 10 * time.Second}
)

// dateLayouts covers the date formats found across RSS and Atom feeds.
var dateLayouts = []string{
	time.RFC1123Z,
	time.RFC1123,
	"Mon, 02 Jan 2006 15:04:05 -0700",
	"Mon, 2 Jan 2006 15:04:05 -0700",
	time.RFC3339,
	"2006-01-02T15:04:05-07:00",
}

func parseDate(s string) int64 {
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix()
		}
	}
	return 0
}

// ── Feed parsing ──────────────────────────────────────────────────────────────

type parsedItem struct {
	title       string
	link        string
	description string
	pubDate     string
}

// parseFeed tries RSS 2.0 first, falls back to Atom 1.0.
// See DESIGN.md § "Feed Parsing".
func parseFeed(body []byte) ([]parsedItem, error) {
	var rss RSS
	if err := xml.Unmarshal(body, &rss); err == nil && len(rss.Channel.Items) > 0 {
		var items []parsedItem
		for _, i := range rss.Channel.Items {
			items = append(items, parsedItem{i.Title, i.Link, i.Description, i.PubDate})
		}
		return items, nil
	}

	var atom AtomFeed
	if err := xml.Unmarshal(body, &atom); err == nil && len(atom.Entries) > 0 {
		var items []parsedItem
		for _, e := range atom.Entries {
			items = append(items, parsedItem{e.Title, e.link(), e.summary(), e.Updated})
		}
		return items, nil
	}

	return nil, fmt.Errorf("unrecognised feed format")
}

// ── Write path ────────────────────────────────────────────────────────────────

// fetchAndStore fetches one publisher's feed and upserts articles.
// INSERT ON CONFLICT (link) DO NOTHING makes repeated calls idempotent.
//
// Tier 2 upgrade: wrap this call with a Redis SETNX lock per publisher
// before invoking — see DESIGN.md § "Tier 2: Distributed Feed Fetching".
func fetchAndStore(pub Publisher) error {
	resp, err := httpClient.Get(pub.FeedURL)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", pub.Name, err)
	}
	defer resp.Body.Close()

	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read %s: %w", pub.Name, err)
	}

	items, err := parseFeed(buf)
	if err != nil {
		return fmt.Errorf("parse %s: %w", pub.Name, err)
	}

	for _, item := range items {
		if item.link == "" {
			continue
		}
		ts := parseDate(item.pubDate)
		_, err := db.Exec(`
			INSERT INTO articles (publisher_id, title, link, description, pub_date, published_at)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (link) DO NOTHING`,
			pub.ID, item.title, item.link, item.description, item.pubDate, ts,
		)
		if err != nil {
			log.Printf("insert article %s: %v", item.link, err)
		}
	}

	db.Exec(`UPDATE publishers SET last_fetched_at = NOW() WHERE id = $1`, pub.ID)
	return nil
}

// refreshAll loads all publishers and fans out one goroutine per feed.
//
// Limitation: on multiple server instances every server fetches every feed.
// Tier 2 fix: acquire Redis SETNX lock:publisher:{id} before fetchAndStore.
// See DESIGN.md § "Tier 2: Distributed Feed Fetching with Publisher Locks".
func refreshAll() {
	rows, err := db.Query(`SELECT id, name, feed_url FROM publishers`)
	if err != nil {
		log.Printf("load publishers: %v", err)
		return
	}
	defer rows.Close()

	var publishers []Publisher
	for rows.Next() {
		var p Publisher
		rows.Scan(&p.ID, &p.Name, &p.FeedURL)
		publishers = append(publishers, p)
	}

	var wg sync.WaitGroup
	for _, p := range publishers {
		wg.Add(1)
		go func(pub Publisher) {
			defer wg.Done()
			if err := fetchAndStore(pub); err != nil {
				log.Printf("warning: %v", err)
			} else {
				log.Printf("fetched %s", pub.Name)
			}
		}(p)
	}
	wg.Wait()
	log.Println("refresh complete")
}

// ── Read path ─────────────────────────────────────────────────────────────────

// newsHandler returns the latest 200 articles across all publishers.
//
// Limitation: hits DB on every request — collapses under load.
// Tier 1 fix: check Redis cache first (TTL 5 min), fall back to DB on miss.
// Tier 5 fix: replace LIMIT 200 with cursor-based pagination.
// See DESIGN.md § "Tier 1: Caching" and § "Tier 5: Read Path at Scale".
func newsHandler(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query(`
		SELECT a.title, a.link, a.description, a.pub_date, p.name, a.published_at
		FROM articles a
		JOIN publishers p ON a.publisher_id = p.id
		ORDER BY a.published_at DESC
		LIMIT 200`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var articles []Article
	for rows.Next() {
		var a Article
		rows.Scan(&a.Title, &a.Link, &a.Description, &a.PubDate, &a.Source, &a.ParsedTime)
		articles = append(articles, a)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(articles)
}

// refreshHandler triggers an async re-fetch of all feeds.
// TODO: add authentication before exposing this in production.
func refreshHandler(w http.ResponseWriter, r *http.Request) {
	go refreshAll()
	w.WriteHeader(http.StatusAccepted)
	fmt.Fprintln(w, "refresh triggered")
}

// ── Entry point ───────────────────────────────────────────────────────────────

func main() {
	var err error

	// TODO Tier 3+: move DSN to env var; add PgBouncer in front for pooling.
	dsn := "host=localhost port=5433 user=postgres password=postgres dbname=rssfeed sslmode=disable"
	db, err = sql.Open("postgres", dsn)
	if err != nil {
		log.Fatal(err)
	}
	if err = db.Ping(); err != nil {
		log.Fatalf("cannot connect to postgres: %v", err)
	}
	log.Println("connected to postgres")

	// Fetch all feeds on startup, then every 15 minutes.
	// TODO Tier 3: replace ticker with a Kafka-based scheduler so fetching
	// is decoupled from the API server process.
	go refreshAll()
	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		for range ticker.C {
			refreshAll()
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/news", newsHandler)
	mux.HandleFunc("/api/refresh", refreshHandler)
	mux.Handle("/", http.FileServer(http.Dir(".")))

	addr := ":8081"
	log.Printf("RSS aggregator running at http://localhost%s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
