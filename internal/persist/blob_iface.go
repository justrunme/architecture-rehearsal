package persist

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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
	s3Client  *s3.Client
}

// NewS3Blob constructs an S3-compatible blob backend using official AWS SDK v2.
func NewS3Blob(endpoint, bucket, access, secret string) (*S3Blob, error) {
	if endpoint == "" || bucket == "" {
		return nil, fmt.Errorf("s3 endpoint and bucket required")
	}
	endpoint = strings.TrimRight(endpoint, "/")
	httpClient := &http.Client{Timeout: 60 * time.Second}
	opts := s3.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider(access, secret, ""),
		UsePathStyle: true,
		BaseEndpoint: aws.String(endpoint),
		HTTPClient:   httpClient,
	}
	client := s3.New(opts)
	return &S3Blob{
		Endpoint:  endpoint,
		Bucket:    bucket,
		AccessKey: access,
		SecretKey: secret,
		Client:    httpClient,
		s3Client:  client,
	}, nil
}

func (s *S3Blob) key(digest string) string {
	return fmt.Sprintf("blobs/%s/%s/%s", digest[:2], digest[2:4], digest)
}

func (s *S3Blob) Put(data []byte, contentType string) (string, string, error) {
	sum := digestSHA256(data)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	key := s.key(sum)
	_, err := s.s3Client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket:      aws.String(s.Bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return "", "", fmt.Errorf("s3 put: %w", err)
	}
	return sum, s.URI(sum), nil
}

func (s *S3Blob) Get(digest string) ([]byte, error) {
	key := s.key(digest)
	out, err := s.s3Client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(s.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("s3 get: %w", err)
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

func (s *S3Blob) URI(digest string) string {
	return fmt.Sprintf("s3://%s/%s", s.Bucket, s.key(digest))
}

func (s *S3Blob) CreateBucket(ctx context.Context) error {
	_, err := s.s3Client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(s.Bucket),
	})
	if err != nil {
		return fmt.Errorf("s3 create bucket: %w", err)
	}
	return nil
}

func (s *S3Blob) Ready() error {
	_, err := s.s3Client.HeadBucket(context.Background(), &s3.HeadBucketInput{
		Bucket: aws.String(s.Bucket),
	})
	if err != nil {
		return fmt.Errorf("s3 ready: %w", err)
	}
	return nil
}

func digestSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
