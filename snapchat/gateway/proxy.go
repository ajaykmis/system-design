package main

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// Route defines a path prefix → backend mapping.
type Route struct {
	Prefix      string
	Backend     string
	StripPrefix string // prefix to strip (e.g. "/api/v1"), keeping the rest
}

// NewProxy creates a reverse proxy handler that routes requests to backends.
func NewProxy(routes []Route) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, route := range routes {
			if strings.HasPrefix(r.URL.Path, route.Prefix) {
				target, err := url.Parse(route.Backend)
				if err != nil {
					http.Error(w, "bad backend", http.StatusInternalServerError)
					return
				}

				proxy := httputil.NewSingleHostReverseProxy(target)
				if route.StripPrefix != "" {
					r.URL.Path = strings.TrimPrefix(r.URL.Path, route.StripPrefix)
					if r.URL.Path == "" {
						r.URL.Path = "/"
					}
				}
				r.Host = target.Host
				proxy.ServeHTTP(w, r)
				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"not found"}`))
	})
}
