package crypt

import (
	"context"
	"fmt"
	"io"
	"time"
)

// Entry is a single file-system entry returned by a storage backend.
type Entry struct {
	ID       string
	ParentID string
	Name     string
	IsDir    bool
	Size     int64
	ModTime  time.Time
	Extra    any
}

// Uploader handles streaming uploads to the backend.
type Uploader interface {
	Put(ctx context.Context, parentID, name string, size int64, body io.Reader) (Entry, error)
}

// EncryptPutRequest describes a one-shot streaming encrypt-and-upload.
type EncryptPutRequest struct {
	Reader    io.Reader
	PlainSize int64
	PlainName string
	ParentID  string
	Nonce     [FileNonceSize]byte
}

// EncryptPutResult reports the outcome of EncryptAndPut.
type EncryptPutResult struct {
	Entry         Entry
	Nonce         [FileNonceSize]byte
	EncryptedSize int64
}

func EncryptAndPut(ctx context.Context, up Uploader, cp Cipher, req EncryptPutRequest) (EncryptPutResult, error) {
	nonce := req.Nonce
	if isZeroNonce(nonce) {
		var err error
		nonce, err = cp.GenerateRandomNonce()
		if err != nil {
			return EncryptPutResult{}, fmt.Errorf("generate nonce: %w", err)
		}
	}

	encName := cp.EncryptSegment(req.PlainName)
	encSize := cp.EncryptedSize(req.PlainSize)
	encReader := NewEncryptingReader(req.Reader, cp, nonce, req.PlainSize)

	entry, err := up.Put(ctx, req.ParentID, encName, encSize, encReader)
	if err != nil {
		return EncryptPutResult{Nonce: nonce, EncryptedSize: encSize}, err
	}
	return EncryptPutResult{Entry: entry, Nonce: nonce, EncryptedSize: encSize}, nil
}

func isZeroNonce(n [FileNonceSize]byte) bool {
	for _, b := range n {
		if b != 0 {
			return false
		}
	}
	return true
}
