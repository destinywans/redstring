// Demo creates real HTTP business traffic and counters. MODE=regression returns 500.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

func main() {
	if os.Getenv("ROLE") == "traffic" {
		c := http.Client{Timeout: 2 * time.Second}
		for {
			r, e := c.Get("http://checkout:8080/work")
			if e == nil {
				io.Copy(io.Discard, r.Body)
				r.Body.Close()
				log.Printf("business_request status=%d", r.StatusCode)
			} else {
				log.Printf("business_request error=%v", e)
			}
			time.Sleep(time.Second)
		}
	}
	var requests, failures atomic.Uint64
	mode := os.Getenv("MODE")
	instance := os.Getenv("HOSTNAME")
	mux := http.NewServeMux()
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ready") })
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if mode == "regression" {
			w.WriteHeader(503)
			fmt.Fprintln(w, "checkout dependency configuration invalid")
			return
		}
		fmt.Fprintln(w, "healthy")
	})
	mux.HandleFunc("/work", func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		status := 200
		if mode == "regression" {
			failures.Add(1)
			status = 500
		}
		log.Printf("checkout_request status=%d mode=%s", status, mode)
		w.WriteHeader(status)
		fmt.Fprintf(w, "checkout status=%d\n", status)
	})
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"requests": requests.Load(), "errors": failures.Load(), "instance": instance})
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# TYPE checkout_requests_total counter\ncheckout_requests_total %d\n# TYPE checkout_errors_total counter\ncheckout_errors_total %d\n", requests.Load(), failures.Load())
	})
	log.Printf("checkout startup mode=%s instance=%s", mode, instance)
	log.Fatal(http.ListenAndServe(":8080", mux))
}
