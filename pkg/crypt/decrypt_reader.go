package crypt

import (
	"fmt"
	"io"
)

type DecryptingReader struct {
	enc        io.Reader
	cipher     Cipher
	nonce      [FileNonceSize]byte
	blockIndex uint64
	pending    []byte
	encEOF     bool
}

func NewDecryptingReader(enc io.Reader, cipher Cipher, nonce [FileNonceSize]byte) *DecryptingReader {
	return &DecryptingReader{
		enc:    enc,
		cipher: cipher,
		nonce:  nonce,
	}
}

func (r *DecryptingReader) Read(p []byte) (int, error) {
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

func (r *DecryptingReader) fillPending() error {
	if r.encEOF {
		return io.EOF
	}

	encBlock := make([]byte, BlockSize)
	n, err := io.ReadFull(r.enc, encBlock)

	if err != nil {
		if err == io.EOF {
			if n == 0 {
				r.encEOF = true
				return io.EOF
			}
		} else if err != io.ErrUnexpectedEOF {
			return fmt.Errorf("read encrypted block: %w", err)
		}
	}

	if n == 0 {
		r.encEOF = true
		return io.EOF
	}

	encBlock = encBlock[:n]

	plaintext, err := r.cipher.DecryptBlock(encBlock, r.blockIndex, r.nonce)
	if err != nil {
		return fmt.Errorf("decrypt block %d: %w", r.blockIndex, err)
	}

	r.blockIndex++
	r.pending = plaintext

	return nil
}
