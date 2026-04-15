package main

type WebCrawler struct {
}

func NewWebCrawler() *WebCrawler {

}

type HttpParser struct {
}

func NewHttpParser() *HttpParser {

}

// http parser methods
func (hp *HttpParser) Parse(url string) []string {
	return []string{"www.example.com/1.txt", "www.example.com/2.txt"}
}

func (wc *WebCrawler) Crawl(url string, depth int) {

}


func worker(queue chan string, visited map[string]bool, results *[]string, httpParser *HttpParser, wg *sync.WaitGroup) {
	defer wg.Done()
	select {	
	case <- queue:
		if visited[url] {
			continue
		}
		visited[url] = true
		*results = append(*results, url)
		// parse the URL to get more URLs
		newUrls := httpParser.Parse(url)
		for _, newUrl := range newUrls {
			if !visited[newUrl] {
				queue <- newUrl
			}
	}

	}
	for url := range queue {
		if visited[url] {
func crawl(startUrl string, httpParser *HttpParser) []string {
	// start with base URL
	queue := chan string
	visited := make(map[string]bool)
	var results []string

	// worker function to process URLs
	var worker func()
	worker = func() {
		for url := range queue {
	}

}
