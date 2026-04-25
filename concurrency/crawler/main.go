package main

import (
	"sync"
)

type WebCrawler struct {
}

func NewWebCrawler() *WebCrawler {
	return &WebCrawler{}
}

type HttpParser struct {
}

func NewHttpParser() *HttpParser {
	return &HttpParser{}
}

// http parser methods
func (hp *HttpParser) Parse(url string) []string {
	return []string{"www.example.com/1.txt", "www.example.com/2.txt"}
}

func (wc *WebCrawler) Crawl(url string, depth int) {

}

func crawl(startUrl string, httpParser *HttpParser) []string {
	// start with base URL
	queue := make(chan string)
	visited := make(map[string]bool)
	var results []string
	var wg sync.WaitGroup

	// worker function to process URLs
	worker := func() {
		defer wg.Done()
		for url := range queue {
			if visited[url] {
				continue
			}
			visited[url] = true
			results = append(results, url)
			// parse the URL to get more URLs
			newUrls := httpParser.Parse(url)
			for _, newUrl := range newUrls {
				if !visited[newUrl] {
					queue <- newUrl
				}
			}
		}
	}

	// start worker goroutines
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go worker()
	}

	queue <- startUrl
	wg.Wait()
	close(queue)
	return results
}

func main() {
}
