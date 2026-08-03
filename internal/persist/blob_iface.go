package persist

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Blob is the content-addressed storage interface (FS or S3-compatible).
type Blob interface {
	Put(data []byte, contentType string) (digest string, uri string, err error)
	Get(digest string) ([]byte, error)
	URI(digest string) string
	Ready() error
}

// PutURI wraps BlobStore.Put to return URI as second value (interface adapter).
func (b *BlobStore) PutURI(data []byte, contentType string) (digest, uri string, err error) {
	d, _, err := b.Put(data, contentType)
	if err != nil {
		return "", "", err
	}
	return d, b.URI(d), nil
}

// URI implements stable reference for filesystem blobs.
func (b *BlobStore) URI(digest string) string {
	if len(digest) < 4 {
		return ""
	}
	return "sha256:" + digest
}

// Ready checks blob root is configured.
func (b *BlobStore) Ready() error {
	if b == nil || b.Root == "" {
		return fmt.Errorf("blob store not configured")
	}
	return nil
}

// S3Blob is a minimal S3-compatible (path-style) content-addressed store.
type S3Blob struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
	Client    *http.Client
}

// NewS3Blob constructs an S3-compatible blob backend.
func NewS3Blob(endpoint, bucket, access, secret string) (*S3Blob, error) {
	if endpoint == "" || bucket == "" {
		return nil, fmt.Errorf("s3 endpoint and bucket required")
	}
	endpoint = strings.TrimRight(endpoint, "/")
	return &S3Blob{
		Endpoint:  endpoint,
		Bucket:    bucket,
		AccessKey: access,
		SecretKey: secret,
		Client:    &http.Client{Timeout: 60 * time.Second},
	}, nil
}

func (s *S3Blob) key(digest string) string {
	return fmt.Sprintf("blobs/%s/%s/%s", digest[:2], digest[2:4], digest)
}

func (s *S3Blob) Put(data []byte, contentType string) (string, string, error) {
	sum := digestSHA256(data)
	url := fmt.Sprintf("%s/%s/%s", s.Endpoint, s.Bucket, s.key(sum))
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		return "", "", err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	req.Header.Set("Content-Type", contentType)
	if s.AccessKey != "" {
		req.SetBasicAuth(s.AccessKey, s.SecretKey)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", "", fmt.Errorf("s3 put %d: %s", resp.StatusCode, b)
	}
	return sum, s.URI(sum), nil
}

func (s *S3Blob) Get(digest string) ([]byte, error) {
	url := fmt.Sprintf("%s/%s/%s", s.Endpoint, s.Bucket, s.key(digest))
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if s.AccessKey != "" {
		req.SetBasicAuth(s.AccessKey, s.SecretKey)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("s3 get %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func (s *S3Blob) URI(digest string) string {
	return fmt.Sprintf("s3://%s/%s", s.Bucket, s.key(digest))
}

func (s *S3Blob) Ready() error {
	req, err := http.NewRequest(http.MethodHead, fmt.Sprintf("%s/%s", s.Endpoint, s.Bucket), nil)
	if err != nil {
		return err
	}
	if s.AccessKey != "" {
		req.SetBasicAuth(s.AccessKey, s.SecretKey)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("s3 not ready: %d", resp.StatusCode)
	}
	return nil
}

func digestSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
