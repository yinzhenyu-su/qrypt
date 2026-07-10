package crypt

import (
	"context"
	"errors"
	"io"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type encryptedReadOnlyFileSource struct {
	plain  drive.ReadOnlyFileSource
	cipher Cipher
	nonce  [FileNonceSize]byte
	hashes drive.SourceHashes
}

func newEncryptedReadOnlyFileSource(plain drive.ReadOnlyFileSource, cipher Cipher, nonce [FileNonceSize]byte, hashes drive.SourceHashes) drive.ReadOnlyFileSource {
	return encryptedReadOnlyFileSource{
		plain:  plain,
		cipher: cipher,
		nonce:  nonce,
		hashes: hashes,
	}
}

func (s encryptedReadOnlyFileSource) Size() int64 {
	return s.cipher.EncryptedSize(s.plain.Size())
}

func (s encryptedReadOnlyFileSource) Open(ctx context.Context) (drive.ReadOnlyFile, error) {
	plain, err := s.plain.Open(ctx)
	if err != nil {
		return nil, err
	}
	return &encryptedReadOnlyFile{
		plain:  plain,
		source: s,
		size:   s.Size(),
	}, nil
}

func (s encryptedReadOnlyFileSource) Hash(algorithm drive.HashAlgorithm) ([]byte, bool) {
	sum, ok := s.hashes[algorithm]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), sum...), true
}

type encryptedReadOnlyFile struct {
	plain  drive.ReadOnlyFile
	source encryptedReadOnlyFileSource
	size   int64
	pos    int64
}

func (f *encryptedReadOnlyFile) Read(p []byte) (int, error) {
	n, err := f.ReadAt(p, f.pos)
	f.pos += int64(n)
	if err == io.EOF && n > 0 {
		return n, nil
	}
	return n, err
}

func (f *encryptedReadOnlyFile) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("crypt: negative encrypted source offset")
	}
	if len(p) == 0 {
		return 0, nil
	}
	if off >= f.size {
		return 0, io.EOF
	}
	if int64(len(p)) > f.size-off {
		p = p[:f.size-off]
	}
	written := 0
	for len(p) > 0 {
		n, err := f.readAtNonEmpty(p, off)
		written += n
		off += int64(n)
		p = p[n:]
		if err != nil {
			if err == io.EOF && written > 0 {
				return written, nil
			}
			return written, err
		}
	}
	return written, nil
}

func (f *encryptedReadOnlyFile) readAtNonEmpty(p []byte, off int64) (int, error) {
	if off < int64(FileHeaderSize) {
		header := f.header()
		n := copy(p, header[off:])
		return n, nil
	}

	rel := off - int64(FileHeaderSize)
	blockIndex := rel / int64(BlockSize)
	blockOffset := rel % int64(BlockSize)
	plainOffset := blockIndex * int64(BlockDataSize)
	plainSize := f.source.plain.Size()
	if plainOffset >= plainSize {
		return 0, io.EOF
	}
	plainBlockSize := int64(BlockDataSize)
	if remaining := plainSize - plainOffset; remaining < plainBlockSize {
		plainBlockSize = remaining
	}
	plainBlock := make([]byte, plainBlockSize)
	if err := readFullAt(f.plain, plainBlock, plainOffset); err != nil {
		return 0, err
	}
	encBlock, err := f.source.cipher.EncryptBlock(plainBlock, uint64(blockIndex), f.source.nonce)
	if err != nil {
		return 0, err
	}
	if blockOffset >= int64(len(encBlock)) {
		return 0, io.EOF
	}
	n := copy(p, encBlock[blockOffset:])
	return n, nil
}

func (f *encryptedReadOnlyFile) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = f.pos + offset
	case io.SeekEnd:
		next = f.size + offset
	default:
		return 0, errors.New("crypt: invalid encrypted source seek whence")
	}
	if next < 0 {
		return 0, errors.New("crypt: negative encrypted source position")
	}
	f.pos = next
	return next, nil
}

func (f *encryptedReadOnlyFile) Close() error {
	return f.plain.Close()
}

func (f *encryptedReadOnlyFile) header() []byte {
	header := make([]byte, 0, FileHeaderSize)
	header = append(header, []byte(FileMagic)...)
	header = append(header, f.source.nonce[:]...)
	return header
}

func readFullAt(r io.ReaderAt, p []byte, off int64) error {
	n, err := r.ReadAt(p, off)
	if err == nil {
		return nil
	}
	if err == io.EOF && n == len(p) {
		return nil
	}
	return err
}
