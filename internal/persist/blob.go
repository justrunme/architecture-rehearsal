package persist

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// BlobStore is a content-addressed local filesystem store (S3-compatible API later).
type BlobStore struct {
	Root string
}

// NewBlobStore creates the root directory.
func NewBlobStore(root string) (*BlobStore, error) {
	if root == "" {
		return nil, fmt.Errorf("blob root required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &BlobStore{Root: root}, nil
}

// Put writes bytes and returns sha256 hex digest.
func (b *BlobStore) Put(data []byte, contentType string) (digest string, path string, err error) {
	sum := sha256.Sum256(data)
	digest = hex.EncodeToString(sum[:])
	// shard: ab/cd/<digest>
	dir := filepath.Join(b.Root, digest[:2], digest[2:4])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	path = filepath.Join(dir, digest)
	if _, err := os.Stat(path); err == nil {
		return digest, path, nil // already stored
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", "", err
	}
	_ = contentType
	_ = time.Now()
	return digest, path, nil
}

// Get reads by digest.
func (b *BlobStore) Get(digest string) ([]byte, error) {
	if len(digest) < 4 {
		return nil, fmt.Errorf("invalid digest")
	}
	path := filepath.Join(b.Root, digest[:2], digest[2:4], digest)
	return os.ReadFile(path)
}

// Path returns filesystem path for digest.
func (b *BlobStore) Path(digest string) string {
	if len(digest) < 4 {
		return ""
	}
	return filepath.Join(b.Root, digest[:2], digest[2:4], digest)
}
