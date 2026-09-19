// collector is a small demo-only forwarder, not a production logging daemon.
// It copies real timestamped checkout Pod logs to Loki's HTTP push API.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type collector struct {
	client *http.Client
	token  string
	api    string
	loki   string
	last   map[string]time.Time
}

func main() {
	const sa = "/var/run/secrets/kubernetes.io/serviceaccount/"
	pem, e := os.ReadFile(sa + "ca.crt")
	if e != nil {
		log.Fatal(e)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		log.Fatal("invalid Kubernetes CA")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	c := collector{client: &http.Client{Transport: tr, Timeout: 10 * time.Second}, token: sa + "token", api: "https://kubernetes.default.svc", loki: "http://canvas-loki:3100", last: map[string]time.Time{}}
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		e := c.collect(ctx)
		cancel()
		if e != nil {
			log.Printf("collection failed: %v", e)
		}
		time.Sleep(5 * time.Second)
	}
}
func (c *collector) get(ctx context.Context, path string) ([]byte, error) {
	req, e := http.NewRequestWithContext(ctx, "GET", c.api+path, nil)
	if e != nil {
		return nil, e
	}
	token, e := os.ReadFile(c.token)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	resp, e := c.client.Do(req)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Kubernetes HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 2<<20))
}
func (c *collector) collect(ctx context.Context) error {
	b, e := c.get(ctx, "/api/v1/namespaces/aiops-demo/pods?labelSelector=app%3Dcheckout")
	if e != nil {
		return e
	}
	var pods struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if e = json.Unmarshal(b, &pods); e != nil {
		return e
	}
	for _, pod := range pods.Items {
		if e = c.pod(ctx, pod.Metadata.Name); e != nil {
			return e
		}
	}
	return nil
}
func (c *collector) pod(ctx context.Context, name string) error {
	since := c.last[name]
	if since.IsZero() {
		since = time.Now().Add(-2 * time.Minute)
	}
	q := url.Values{"timestamps": {"true"}, "sinceTime": {since.Format(time.RFC3339Nano)}, "tailLines": {"2000"}, "limitBytes": {"1048576"}}
	b, e := c.get(ctx, "/api/v1/namespaces/aiops-demo/pods/"+name+"/log?"+q.Encode())
	if e != nil {
		return e
	}
	values := [][2]string{}
	latest := since
	scanner := bufio.NewScanner(bytes.NewReader(b))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		stamp, line, ok := strings.Cut(scanner.Text(), " ")
		if !ok {
			continue
		}
		at, e := time.Parse(time.RFC3339Nano, stamp)
		if e != nil || !at.After(c.last[name]) {
			continue
		}
		values = append(values, [2]string{strconv.FormatInt(at.UnixNano(), 10), line})
		if at.After(latest) {
			latest = at
		}
	}
	if e = scanner.Err(); e != nil {
		return e
	}
	if len(values) == 0 {
		return nil
	}
	payload := map[string]any{"streams": []any{map[string]any{"stream": map[string]string{"application": "demo", "namespace": "aiops-demo", "app": "checkout", "pod": name}, "values": values}}}
	raw, e := json.Marshal(payload)
	if e != nil {
		return e
	}
	req, e := http.NewRequestWithContext(ctx, "POST", c.loki+"/loki/api/v1/push", bytes.NewReader(raw))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	resp, e := c.client.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != 204 {
		return fmt.Errorf("Loki push HTTP %d", resp.StatusCode)
	}
	c.last[name] = latest
	log.Printf("forwarded pod=%s entries=%d", name, len(values))
	return nil
}
