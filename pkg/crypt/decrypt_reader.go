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
	// remaining is how many ciphertext bytes the caller expects; -1 means the
	// length is unknown, so a short final block is accepted as written.
	remaining int64
}

func NewDecryptingReader(enc io.Reader, cipher Cipher, nonce [FileNonceSize]byte) *DecryptingReader {
	return &DecryptingReader{
		enc:       enc,
		cipher:    cipher,
		nonce:     nonce,
		remaining: -1,
	}
}

func NewDecryptingReaderAt(enc io.Reader, cipher Cipher, nonce [FileNonceSize]byte, blockIndex uint64) *DecryptingReader {
	return &DecryptingReader{
		enc:        enc,
		cipher:     cipher,
		nonce:      nonce,
		blockIndex: blockIndex,
		remaining:  -1,
	}
}

// NewDecryptingReaderAtLimit is NewDecryptingReaderAt for a caller that knows
// exactly how many ciphertext bytes the body should carry. A body that ends
// before that count is a truncated read, not a short final block, so it is
// reported as io.ErrUnexpectedEOF and can be replayed by the read layer.
// A negative encSize keeps the unknown-length semantics.
func NewDecryptingReaderAtLimit(enc io.Reader, cipher Cipher, nonce [FileNonceSize]byte, blockIndex uint64, encSize int64) *DecryptingReader {
	reader := NewDecryptingReaderAt(enc, cipher, nonce, blockIndex)
	reader.remaining = encryptedSizeLimit(encSize)
	return reader
}

// NewDecryptingReaderLimit is NewDecryptingReader for a caller that knows the
// remaining ciphertext length. A negative length means "unknown": the stream is
// then read until EOF and a short final block is accepted, because a backend
// that cannot report a size must still yield its content.
func NewDecryptingReaderLimit(enc io.Reader, cipher Cipher, nonce [FileNonceSize]byte, encSize int64) *DecryptingReader {
	reader := NewDecryptingReader(enc, cipher, nonce)
	reader.remaining = encryptedSizeLimit(encSize)
	return reader
}

// encryptedSizeLimit keeps a negative length as the unknown sentinel. Clamping
// it to zero would make the reader stop right after the header and report the
// whole file as empty, which a downloader records as a successful empty file.
func encryptedSizeLimit(encSize int64) int64 {
	if encSize < 0 {
		return -1
	}
	return encSize
}

func (r *DecryptingReader) Read(p []byte) (int, error) {
	total := 0
	for total < len(p) {
		if len(r.pending) == 0 {
			if err := r.fillPendingUploads(); err != nil {
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

func (r *DecryptingReader) fillPendingUploads() error {
	if r.encEOF {
		return io.EOF
	}

	blockSize := int64(BlockSize)
	if r.remaining >= 0 {
		if r.remaining == 0 {
			r.encEOF = true
			return io.EOF
		}
		if r.remaining < blockSize {
			blockSize = r.remaining
		}
	}

	encBlock := make([]byte, blockSize)
	n, err := io.ReadFull(r.enc, encBlock)

	if err != nil {
		if err == io.EOF && n == 0 {
			if r.remaining > 0 {
				// The caller stated how much ciphertext this stream carries and
				// bytes are still owed. An empty body here is a truncated
				// response (or one that stops exactly on a block boundary), not
				// the end of the file: report it so the read layer can replay
				// it, instead of handing back a silently short file.
				return io.ErrUnexpectedEOF
			}
			r.encEOF = true
			return io.EOF
		}
		if err == io.ErrUnexpectedEOF {
			if r.remaining >= 0 {
				// The caller knows how much ciphertext should be here, so a
				// short block means the body ended prematurely. Decrypting it
				// would fail authentication and hide the truncation.
				return io.ErrUnexpectedEOF
			}
			// Unknown total length: the last block of the file is short by
			// design (rclone does not pad it).
		} else {
			return fmt.Errorf("read encrypted block: %w", err)
		}
	}

	if n == 0 {
		r.encEOF = true
		return io.EOF
	}

	if r.remaining >= 0 {
		r.remaining -= int64(n)
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
