package crypt

import (
	"io"
)

type EncryptingReader struct {
	plain        io.Reader
	cipher       Cipher
	nonce        [FileNonceSize]byte
	remaining    int64
	headerSent   bool
	blockIndex   uint64
	pending      []byte
	plaintextEOF bool
}

func NewEncryptingReader(plain io.Reader, cipher Cipher, nonce [FileNonceSize]byte, plainSize int64) *EncryptingReader {
	return &EncryptingReader{
		plain:     plain,
		cipher:    cipher,
		nonce:     nonce,
		remaining: plainSize,
	}
}

func (r *EncryptingReader) Read(p []byte) (int, error) {
	total := 0
	for total < len(p) {
		if len(r.pending) == 0 {
			if err := r.fillPending(); err != nil {
				if err == io.EOF && total > 0 {
					return total, nil
				}
				return total, err
			}
		}

		n := copy(p[total:], r.pending)
		total += n
		r.pending = r.pending[n:]
	}
	return total, nil
}

func (r *EncryptingReader) fillPending() error {
	if !r.headerSent {
		header := make([]byte, 0, FileHeaderSize)
		header = append(header, []byte(FileMagic)...)
		header = append(header, r.nonce[:]...)
		r.pending = header
		r.headerSent = true
		return nil
	}

	if r.plaintextEOF {
		return io.EOF
	}

	if r.remaining <= 0 {
		r.plaintextEOF = true
		return io.EOF
	}

	chunkSize := int64(BlockDataSize)
	if r.remaining < chunkSize {
		chunkSize = r.remaining
	}

	plain := make([]byte, chunkSize)
	n, err := io.ReadFull(r.plain, plain)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return err
	}
	if n == 0 {
		r.plaintextEOF = true
		return io.EOF
	}

	plain = plain[:n]
	r.remaining -= int64(n)
	if r.remaining == 0 {
		r.plaintextEOF = true
	}

	ciphertext, err := r.cipher.EncryptBlock(plain, r.blockIndex, r.nonce)
	if err != nil {
		return err
	}
	r.blockIndex++
	r.pending = ciphertext
	return nil
}
