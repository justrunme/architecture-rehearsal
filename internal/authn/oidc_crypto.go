package authn

import (
	"crypto"
	"crypto/sha256"
)

func sha256Sum(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

func cryptoHashSHA256() crypto.Hash { return crypto.SHA256 }
