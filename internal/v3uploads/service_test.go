package v3uploads

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJSONLinkAndPasteAreInert(t *testing.T) {
	link, content, err := parseJSON(bytes.NewBufferString(`{"kind":"link","name":"example","url":"https://example.com/path","size":0,"mime":"text/uri-list"}`))
	if err != nil || link.URL != "https://example.com/path" || content != nil {
		t.Fatalf("link: %#v %v", link, err)
	}
	for _, input := range []string{
		`{"kind":"link","name":"x","url":"file:///etc/passwd"}`,
		`{"kind":"link","name":"x","url":"https://user:pass@example.com"}`,
		`{"kind":"file","name":"x","size":1}`,
		`{"kind":"paste","name":"x","text":"a","size":2}`,
	} {
		if _, _, err := parseJSON(bytes.NewBufferString(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	paste, data, err := parseJSON(bytes.NewBufferString(`{"kind":"paste","name":"note","text":"你好","size":6,"mime":"text/plain"}`))
	if err != nil || paste.Size != 6 || string(data) != "你好" {
		t.Fatalf("paste: %#v %q %v", paste, data, err)
	}
}

func TestMultipartTypeAndSize(t *testing.T) {
	makeRequest := func(name string, contents []byte, kind string) *http.Request {
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		_ = writer.WriteField("kind", kind)
		file, _ := writer.CreateFormFile("file", name)
		_, _ = file.Write(contents)
		_ = writer.Close()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", &buf)
		r.Header.Set("Content-Type", writer.FormDataContentType())
		return r
	}
	v, _, err := parseMultipart(makeRequest("trace.txt", []byte("hello"), "file"))
	if err != nil || v.MIME != "text/plain" || v.Size != 5 {
		t.Fatalf("plain text: %#v %v", v, err)
	}
	if _, _, err := parseMultipart(makeRequest("script.svg", []byte("<svg></svg>"), "image")); !errors.Is(err, errUnsupported) {
		t.Fatalf("SVG should be unsupported: %v", err)
	}
	if _, _, err := parseMultipart(makeRequest("trace.txt", bytes.Repeat([]byte{'x'}, MaxUpload+1), "file")); !errors.Is(err, errTooLarge) {
		t.Fatalf("large file: %v", err)
	}
}

func TestMissingEnvironmentFailsClosedBeforeStorage(t *testing.T) {
	s := New(nil)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", bytes.NewBufferString(`{"kind":"link","name":"x","url":"https://example.com"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.upload(w, r)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status: %d, body: %s", w.Code, w.Body.String())
	}
	var body map[string]map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["error"]["code"] != "environment_required" {
		t.Fatalf("error: %v %v", body, err)
	}
}
