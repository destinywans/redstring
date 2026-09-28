package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJenkinsReadScopedBuilds(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("private-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	called := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		if r.Method != http.MethodGet || r.URL.Path != "/jenkins/job/Deploy/job/prod%20web/api/json" && r.URL.EscapedPath() != "/jenkins/job/Deploy/job/prod%20web/api/json" {
			t.Errorf("unexpected request method/path: %s %s", r.Method, r.URL.EscapedPath())
		}
		if got := r.URL.Query().Get("tree"); got != "fullName,builds[number,result,building,timestamp,duration]{0,2}" {
			t.Errorf("unexpected tree: %s", got)
		}
		user, password, ok := r.BasicAuth()
		if !ok || user != "reader" || password != "private-token" {
			t.Error("missing or wrong scoped basic authentication")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"fullName":"Deploy/prod web","url":"https://jenkins.invalid/secret","builds":[{"number":12,"result":"SUCCESS","building":false,"timestamp":123456789,"duration":500,"actions":[{"secret":"hidden"}]},{"number":11,"result":null,"building":true,"timestamp":123456000,"duration":0}]}`))
	}))
	defer server.Close()
	client, err := NewJenkins(JenkinsConfig{
		BaseURL: server.URL + "/jenkins", Jobs: map[string]string{"release": "Deploy/prod web"},
		AuthMode: "basic", Username: "reader", TokenFile: tokenPath, AllowHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListJenkinsBuilds(context.Background(), "other", 2); !errors.Is(err, ErrJenkinsJob) {
		t.Fatalf("unconfigured job: %v", err)
	}
	if _, err := client.ListJenkinsBuilds(context.Background(), "release", 51); !errors.Is(err, ErrJenkinsLimit) {
		t.Fatalf("unbounded query: %v", err)
	}
	if called != 0 {
		t.Fatal("invalid selection caused an upstream request")
	}
	view, err := client.ListJenkinsBuilds(context.Background(), "release", 2)
	if err != nil {
		t.Fatal(err)
	}
	if called != 1 || view.Alias != "release" || len(view.Builds) != 2 || view.Builds[0].Number != 12 ||
		view.Builds[0].TimestampMS != 123456789 || view.Builds[0].DurationMS != 500 || !view.Builds[1].Building {
		t.Fatalf("unexpected build projection: %+v; calls=%d", view, called)
	}
	raw, err := json.Marshal(view)
	if err != nil || strings.Contains(string(raw), "jenkins.invalid") || strings.Contains(string(raw), "hidden") || strings.Contains(string(raw), "private-token") {
		t.Fatalf("response leaked upstream fields: %s; err=%v", raw, err)
	}
}

// This opt-in check exercises Jenkins's actual tree query parser and build
// fields. Supply a temporary local Jenkins URL and a job with at least one run.
func TestJenkinsLiveReadOnly(t *testing.T) {
	endpoint := os.Getenv("JENKINS_READCHECK_URL")
	job := os.Getenv("JENKINS_READCHECK_JOB")
	if endpoint == "" || job == "" {
		t.Skip("temporary Jenkins endpoint and job not configured")
	}
	client, err := NewJenkins(JenkinsConfig{
		BaseURL: endpoint, Jobs: map[string]string{"readcheck": job}, AllowHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := client.ListJenkinsBuilds(context.Background(), "readcheck", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Builds) == 0 || view.Builds[0].Number < 1 || view.Builds[0].TimestampMS <= 0 {
		t.Fatalf("Jenkins did not return a real build: %+v", view)
	}
}

func TestJenkinsRejectsUnsafeConfig(t *testing.T) {
	valid := JenkinsConfig{BaseURL: "https://ci.example.invalid/jenkins", Jobs: map[string]string{"release": "Deploy/prod"}}
	cases := []JenkinsConfig{
		{BaseURL: "http://ci.example.invalid", Jobs: valid.Jobs, AllowHTTP: true},
		{BaseURL: "http://127.0.0.1", Jobs: valid.Jobs},
		{BaseURL: "https://user:secret@ci.example.invalid", Jobs: valid.Jobs},
		{BaseURL: "https://ci.example.invalid/?token=secret", Jobs: valid.Jobs},
		{BaseURL: "https://ci.example.invalid?", Jobs: valid.Jobs},
		{BaseURL: "https://ci.example.invalid/%2e%2e", Jobs: valid.Jobs},
		{BaseURL: valid.BaseURL, Jobs: map[string]string{"release": "../other"}},
		{BaseURL: valid.BaseURL, Jobs: map[string]string{"release": "Deploy//prod"}},
		{BaseURL: valid.BaseURL, Jobs: map[string]string{"release": "Deploy/%2Fprod"}},
		{BaseURL: valid.BaseURL, Jobs: valid.Jobs, AuthMode: "basic", Username: "reader", TokenFile: "relative"},
		{BaseURL: valid.BaseURL, Jobs: valid.Jobs, AuthMode: "basic", Username: "alice:other", TokenFile: "/tmp/token"},
	}
	for _, config := range cases {
		if _, err := NewJenkins(config); !errors.Is(err, ErrJenkinsConfig) {
			t.Errorf("accepted unsafe config: %+v, err=%v", config, err)
		}
	}
	if _, err := NewJenkins(valid); err != nil {
		t.Fatalf("valid HTTPS config: %v", err)
	}
}

func TestJenkinsRejectsRedirectOversizeAndWrongJob(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(secretPath, []byte("secret-token"), 0600); err != nil {
		t.Fatal(err)
	}
	redirectReached := false
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirectReached = true }))
	defer other.Close()
	mode := "redirect"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Error("bearer credential missing")
		}
		switch mode {
		case "redirect":
			w.Header().Set("Location", other.URL+"/steal")
			w.WriteHeader(http.StatusFound)
		case "large":
			_, _ = w.Write([]byte(strings.Repeat("x", jenkinsMaxResponseBytes+1)))
		case "wrong-job":
			_, _ = w.Write([]byte(`{"fullName":"Deploy/other","builds":[]}`))
		case "invalid-build":
			_, _ = w.Write([]byte(`{"fullName":"Deploy/prod","builds":[{"number":1,"result":"SECRET","timestamp":1}]}`))
		case "missing-fields":
			_, _ = w.Write([]byte(`{"fullName":"Deploy/prod","builds":[{"number":1,"result":"SUCCESS","building":false,"duration":1}]}`))
		case "null-fields":
			_, _ = w.Write([]byte(`{"fullName":"Deploy/prod","builds":[{"number":1,"result":"SUCCESS","building":null,"timestamp":1,"duration":1}]}`))
		case "future-time":
			_, _ = fmt.Fprintf(w, `{"fullName":"Deploy/prod","builds":[{"number":1,"result":"SUCCESS","building":false,"timestamp":%d,"duration":1}]}`, time.Now().Add(10*time.Minute).UnixMilli())
		case "forbidden":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("secret-token"))
		}
	}))
	defer server.Close()
	client, err := NewJenkins(JenkinsConfig{
		BaseURL: server.URL, Jobs: map[string]string{"release": "Deploy/prod"},
		AuthMode: "bearer", TokenFile: secretPath, AllowHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		mode string
		want error
	}{{"redirect", ErrJenkinsRedirect}, {"large", ErrJenkinsResponseTooLarge},
		{"wrong-job", ErrJenkinsResponse}, {"invalid-build", ErrJenkinsResponse},
		{"missing-fields", ErrJenkinsResponse}, {"null-fields", ErrJenkinsResponse},
		{"future-time", ErrJenkinsResponse}, {"forbidden", ErrJenkinsForbidden}}
	for _, check := range checks {
		mode = check.mode
		_, err := client.ListJenkinsBuilds(context.Background(), "release", 1)
		if !errors.Is(err, check.want) || strings.Contains(err.Error(), "secret-token") {
			t.Errorf("%s: %v", mode, err)
		}
	}
	if redirectReached {
		t.Fatal("followed upstream redirect with credential")
	}
	if err := os.Chmod(secretPath, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListJenkinsBuilds(context.Background(), "release", 1); !errors.Is(err, ErrJenkinsCredential) {
		t.Fatalf("accepted group-readable token: %v", err)
	}
}

func TestJenkinsHonorsRequestDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	client, err := NewJenkins(JenkinsConfig{
		BaseURL: server.URL, Jobs: map[string]string{"job": "codex-readcheck"}, AllowHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := client.ListJenkinsBuilds(ctx, "job", 1); !errors.Is(err, ErrJenkinsTimeout) {
		t.Fatalf("request deadline was not classified as timeout: %v", err)
	}
}
