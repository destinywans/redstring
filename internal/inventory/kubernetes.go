package inventory

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type endpoint struct{ Path, Kind string }

var endpoints = []endpoint{
	{"/api/v1/namespaces", "Namespace"}, {"/api/v1/nodes", "Node"},
	{"/apis/apps/v1/deployments", "Deployment"}, {"/apis/apps/v1/statefulsets", "StatefulSet"}, {"/apis/apps/v1/daemonsets", "DaemonSet"}, {"/apis/apps/v1/replicasets", "ReplicaSet"},
	{"/apis/batch/v1/jobs", "Job"}, {"/apis/batch/v1/cronjobs", "CronJob"},
	{"/api/v1/pods", "Pod"}, {"/api/v1/services", "Service"}, {"/apis/networking.k8s.io/v1/ingresses", "Ingress"},
	{"/api/v1/persistentvolumes", "PersistentVolume"}, {"/api/v1/persistentvolumeclaims", "PersistentVolumeClaim"}, {"/apis/storage.k8s.io/v1/storageclasses", "StorageClass"},
	{"/api/v1/events", "Event"},
}

type object map[string]any
type kubeClient struct {
	source Source
	client *http.Client
}

func newKube(source Source) (*kubeClient, error) {
	u, err := url.Parse(source.APIURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" {
		return nil, fmt.Errorf("inventory API must be HTTPS without credentials/query")
	}
	pem, err := os.ReadFile(source.CAFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("inventory CA invalid")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return &kubeClient{source, &http.Client{Transport: tr, Timeout: 40 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (k *kubeClient) request(ctx context.Context, path string) (*http.Response, error) {
	token, err := os.ReadFile(k.source.TokenFile)
	if err != nil {
		return nil, fmt.Errorf("inventory token unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(k.source.APIURL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	response, err := k.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Kubernetes inventory transport unavailable")
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("Kubernetes inventory HTTP %d for %s", response.StatusCode, strings.Split(path, "?")[0])
	}
	return response, nil
}
func (k *kubeClient) list(ctx context.Context, ep endpoint) ([]object, string, error) {
	items := []object{}
	continuation := ""
	revision := ""
	for pages := 0; pages < 100; pages++ {
		query := url.Values{"limit": {"500"}}
		if continuation != "" {
			query.Set("continue", continuation)
		}
		response, err := k.request(ctx, ep.Path+"?"+query.Encode())
		if err != nil {
			return nil, "", err
		}
		var page struct {
			Metadata struct {
				Continue        string `json:"continue"`
				ResourceVersion string `json:"resourceVersion"`
			} `json:"metadata"`
			Items []object `json:"items"`
		}
		err = json.NewDecoder(io.LimitReader(response.Body, 32<<20)).Decode(&page)
		response.Body.Close()
		if err != nil {
			return nil, "", fmt.Errorf("invalid Kubernetes list for %s", ep.Kind)
		}
		for _, item := range page.Items {
			item["kind"] = ep.Kind
			items = append(items, item)
		}
		if len(items) > 10000 {
			return nil, "", fmt.Errorf("Kubernetes kind exceeds 10000 objects")
		}
		revision = page.Metadata.ResourceVersion
		continuation = page.Metadata.Continue
		if continuation == "" {
			return items, revision, nil
		}
	}
	return nil, "", fmt.Errorf("Kubernetes pagination bound exceeded")
}
func (k *kubeClient) snapshot(ctx context.Context) (Snapshot, map[string]string, error) {
	objects := []object{}
	versions := map[string]string{}
	for _, ep := range endpoints {
		items, rv, err := k.list(ctx, ep)
		if err != nil {
			return Snapshot{}, nil, err
		}
		objects = append(objects, items...)
		versions[ep.Path] = rv
		if len(objects) > 10000 {
			return Snapshot{}, nil, fmt.Errorf("Kubernetes source exceeds 10000 objects")
		}
	}
	snapshot, err := convert(k.source, objects, time.Now().UTC())
	return snapshot, versions, err
}

// Watches are invalidation signals, not authoritative deltas. A reconnect, 410,
// or any object event wakes a bounded full LIST. No missed/deleted watch event
// can delete CMDB data; only a complete subsequent LIST marks absence.
func (k *kubeClient) awaitChange(ctx context.Context, versions map[string]string, interval time.Duration) {
	ctx, cancel := context.WithTimeout(ctx, interval)
	defer cancel()
	wake := make(chan struct{}, 1)
	for _, ep := range endpoints {
		go func(ep endpoint) {
			q := url.Values{"watch": {"true"}, "allowWatchBookmarks": {"true"}, "resourceVersion": {versions[ep.Path]}, "timeoutSeconds": {"25"}}
			response, err := k.request(ctx, ep.Path+"?"+q.Encode())
			if err != nil {
				select {
				case wake <- struct{}{}:
				default:
				}
				return
			}
			defer response.Body.Close()
			decoder := json.NewDecoder(io.LimitReader(response.Body, 32<<20))
			for {
				var event struct {
					Type string `json:"type"`
				}
				if decoder.Decode(&event) != nil {
					select {
					case wake <- struct{}{}:
					default:
					}
					return
				}
				if event.Type != "BOOKMARK" {
					select {
					case wake <- struct{}{}:
					default:
					}
					return
				}
			}
		}(ep)
	}
	select {
	case <-wake:
	case <-ctx.Done():
	}
}
