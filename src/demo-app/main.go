package main

import (
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
)

var version = "dev" // overridden at build time via -ldflags "-X main.version=<sha>"
var httpRequests atomic.Int64

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpRequests.Add(1)
		fmt.Fprintf(w, "hello from demo-app, version %s\n", version)
	})
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprint(w, "# HELP http_requests_total Total requests handled by demo-app.\n")
		fmt.Fprint(w, "# TYPE http_requests_total counter\n")
		fmt.Fprintf(w, "http_requests_total %d\n", httpRequests.Load())
	})
	log.Printf("demo-app %s listening on :8080", version)
	log.Fatal(http.ListenAndServe(":8080", nil))
}
