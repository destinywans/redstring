package delivery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	hashpkg "hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type S3Config struct {
	Endpoint      string `json:"endpoint"`
	Bucket        string `json:"bucket"`
	Region        string `json:"region"`
	AccessKeyFile string `json:"access_key_file"`
	SecretKeyFile string `json:"secret_key_file"`
	AllowHTTP     bool   `json:"allow_http"`
	ReadEndpoint  string `json:"read_endpoint,omitempty"`
}
type S3Store struct {
	config         S3Config
	access, secret string
	client         *http.Client
}

func NewS3Store(config S3Config, access, secret string) (*S3Store, error) {
	if err := validEndpoint(config.Endpoint, config.AllowHTTP); err != nil {
		return nil, err
	}
	if config.ReadEndpoint != "" {
		if err := validEndpoint(config.ReadEndpoint, config.AllowHTTP); err != nil {
			return nil, err
		}
	}
	if config.Bucket == "" || strings.ContainsAny(config.Bucket, "/.\\") || config.Region == "" || access == "" || secret == "" {
		return nil, ErrInvalid
	}
	client := newHTTPClient()
	client.Timeout = 2 * time.Minute
	return &S3Store{config, access, secret, client}, nil
}
func hmacSum(key []byte, s string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(s))
	return h.Sum(nil)
}
func (s *S3Store) request(ctx context.Context, method, key, digest string, body io.Reader, size int64) (*http.Response, error) {
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "..") || strings.ContainsAny(key, "\\\r\n") {
		return nil, ErrInvalid
	}
	u, err := url.Parse(strings.TrimRight(s.config.Endpoint, "/") + "/" + s.config.Bucket + "/" + key)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.ContentLength = size
	now := time.Now().UTC()
	stamp := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	req.Header.Set("X-Amz-Date", stamp)
	req.Header.Set("X-Amz-Content-Sha256", digest)
	req.Header.Set("Accept-Encoding", "identity")
	headers := "host:" + u.Host + "\nx-amz-content-sha256:" + digest + "\nx-amz-date:" + stamp + "\n"
	signed := "host;x-amz-content-sha256;x-amz-date"
	canonical := method + "\n" + u.EscapedPath() + "\n" + u.Query().Encode() + "\n" + headers + "\n" + signed + "\n" + digest
	canonicalHash := sha256.Sum256([]byte(canonical))
	scope := day + "/" + s.config.Region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hex.EncodeToString(canonicalHash[:])
	k := hmacSum([]byte("AWS4"+s.secret), day)
	k = hmacSum(k, s.config.Region)
	k = hmacSum(k, "s3")
	k = hmacSum(k, "aws4_request")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.access+"/"+scope+", SignedHeaders="+signed+", Signature="+hex.EncodeToString(hmacSum(k, toSign)))
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, errors.New("artifact service unavailable")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("artifact service HTTP %d", resp.StatusCode)
	}
	return resp, nil
}
func (s *S3Store) Put(ctx context.Context, prefix string, input io.Reader, size int64) (Artifact, error) {
	if size < 0 || size > 256<<20 || prefix == "" || strings.Contains(prefix, "..") || strings.ContainsAny(prefix, "?&#\\\r\n") {
		return Artifact{}, ErrInvalid
	}
	f, err := os.CreateTemp("", "aiops-delivery-artifact-*")
	if err != nil {
		return Artifact{}, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(input, size+1))
	if err != nil {
		return Artifact{}, err
	}
	if n != size {
		return Artifact{}, ErrInvalid
	}
	digest := hex.EncodeToString(h.Sum(nil))
	key := strings.Trim(prefix, "/") + "/sha256/" + digest
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return Artifact{}, err
	}
	resp, err := s.request(ctx, "PUT", key, digest, f, size)
	if err != nil {
		return Artifact{}, err
	}
	resp.Body.Close()
	return Artifact{Key: key, Digest: "sha256:" + digest, Size: size}, nil
}
func (s *S3Store) Open(ctx context.Context, a Artifact) (io.ReadCloser, error) {
	if !digestPattern.MatchString(a.Digest) || a.Size < 0 || a.Size > 256<<20 || !strings.HasSuffix(a.Key, "/sha256/"+strings.TrimPrefix(a.Digest, "sha256:")) {
		return nil, ErrInvalid
	}
	empty := sha256.Sum256(nil)
	resp, err := s.request(ctx, "GET", a.Key, hex.EncodeToString(empty[:]), nil, 0)
	if err != nil {
		return nil, err
	}
	if resp.ContentLength >= 0 && resp.ContentLength != a.Size {
		resp.Body.Close()
		return nil, errors.New("artifact size changed")
	}
	return &checkedArtifact{body: resp.Body, hash: sha256.New(), want: a.Digest, size: a.Size}, nil
}

type checkedArtifact struct {
	body       io.ReadCloser
	hash       hashpkg.Hash
	want       string
	size, read int64
	done       bool
}

func (r *checkedArtifact) Read(b []byte) (int, error) {
	n, err := r.body.Read(b)
	r.read += int64(n)
	_, _ = r.hash.Write(b[:n])
	if r.read > r.size {
		return n, errors.New("artifact exceeds recorded size")
	}
	if err == io.EOF {
		r.done = true
		if r.read != r.size || "sha256:"+hex.EncodeToString(r.hash.Sum(nil)) != r.want {
			return n, errors.New("artifact integrity mismatch")
		}
	}
	return n, err
}
func (r *checkedArtifact) Close() error { return r.body.Close() }

func (s *S3Store) acceptsCanonical(raw, digest string) bool {
	if !digestPattern.MatchString(digest) || strings.ContainsAny(raw, "?#") {
		return false
	}
	prefix := strings.TrimRight(s.config.Endpoint, "/") + "/" + s.config.Bucket + "/"
	if !strings.HasPrefix(raw, prefix) || !strings.HasSuffix(raw, "/sha256/"+strings.TrimPrefix(digest, "sha256:")) {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.User == nil && !strings.Contains(u.Path, "..")
}
func (s *S3Store) CanonicalURL(a Artifact) string {
	return strings.TrimRight(s.config.Endpoint, "/") + "/" + s.config.Bucket + "/" + a.Key
}
func (s *S3Store) SignedReadURL(raw string) (string, error) {
	if strings.ContainsAny(raw, "?#") || !strings.HasPrefix(raw, strings.TrimRight(s.config.Endpoint, "/")+"/"+s.config.Bucket+"/") {
		return "", ErrInvalid
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || strings.Contains(u.Path, "..") {
		return "", ErrInvalid
	}
	if s.config.ReadEndpoint != "" {
		old, _ := url.Parse(s.config.Endpoint)
		read, _ := url.Parse(s.config.ReadEndpoint)
		u.Scheme = read.Scheme
		u.Host = read.Host
		u.Path = strings.TrimRight(read.Path, "/") + strings.TrimPrefix(u.Path, strings.TrimRight(old.Path, "/"))
		u.RawPath = ""
	}
	// The caller only resolves a VerifiedSource. Argo archived files use stable
	// workflow/node keys; ArtifactStore.Put uses digest keys. Both are immutable
	// approval references whose downloaded bytes are checked by the executor.
	now := time.Now().UTC()
	day := now.Format("20060102")
	scope := day + "/" + s.config.Region + "/s3/aws4_request"
	q := u.Query()
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", s.access+"/"+scope)
	q.Set("X-Amz-Date", now.Format("20060102T150405Z"))
	q.Set("X-Amz-Expires", "900")
	q.Set("X-Amz-SignedHeaders", "host")
	query := strings.ReplaceAll(q.Encode(), "+", "%20")
	canonical := "GET\n" + u.EscapedPath() + "\n" + query + "\nhost:" + u.Host + "\n\nhost\nUNSIGNED-PAYLOAD"
	sum := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + q.Get("X-Amz-Date") + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	k := hmacSum([]byte("AWS4"+s.secret), day)
	k = hmacSum(k, s.config.Region)
	k = hmacSum(k, "s3")
	k = hmacSum(k, "aws4_request")
	q.Set("X-Amz-Signature", hex.EncodeToString(hmacSum(k, toSign)))
	u.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20")
	return u.String(), nil
}
func (s *Service) ResolveArtifactURL(_ context.Context, canonical string) (string, error) {
	object, ok := s.Artifacts.(*S3Store)
	if !ok {
		return "", ErrUnsupported
	}
	return object.SignedReadURL(canonical)
}
