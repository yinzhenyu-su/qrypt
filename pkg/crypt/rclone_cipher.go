// Package cipher provides the rclone-compatible encryption implementation.
//
// RcloneCipher implements the Cipher interface using NaCl secretbox
// for block encryption and AES-EME for filename encryption, matching rclone's
// crypt remote behavior for interoperability.
package crypt

import (
	"bytes"
	"crypto/aes"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/rfjakob/eme"
	"golang.org/x/crypto/nacl/secretbox"
	"golang.org/x/crypto/scrypt"
)

const (
	FileMagic       = "RCLONE\x00\x00"
	FileMagicSize   = len(FileMagic)
	FileNonceSize   = 24
	FileHeaderSize  = FileMagicSize + FileNonceSize
	BlockHeaderSize = 16
	BlockDataSize   = 64 * 1024
	BlockSize       = BlockHeaderSize + BlockDataSize
)

var defaultSalt = []byte{0xA8, 0x0D, 0xF4, 0x3A, 0x8F, 0xBD, 0x03, 0x08, 0xA7, 0xCA, 0xB8, 0x3E, 0x58, 0x1F, 0x86, 0xB1}
var rcloneBase32 = base32.HexEncoding.WithPadding(base32.NoPadding)
var rcloneBase64 = base64.URLEncoding.WithPadding(base64.NoPadding)



const obfuscQuoteRune = '!'

type RcloneCipher struct {
	dataKey            [32]byte
	nameKey            [32]byte
	nameTweak          [16]byte
	filenameEncryption string
	filenameEncoding   string
}

// Cipher is the encryption contract for rclone-compatible crypt operations.
type Cipher interface {
	EncryptSegment(plain string) string
	DecryptSegment(cipher string) (string, error)
	EncryptBlock(plaintext []byte, blockIndex uint64, fileNonce [FileNonceSize]byte) ([]byte, error)
	DecryptBlock(ciphertext []byte, blockIndex uint64, fileNonce [FileNonceSize]byte) ([]byte, error)
	EncryptedSize(plainSize int64) int64
	DecryptedSize(cipherSize int64) (int64, error)
	GenerateRandomNonce() ([FileNonceSize]byte, error)
}

func NewRcloneCipher(password, salt string, opts ...string) (*RcloneCipher, error) {
	saltBytes := defaultSalt
	if salt != "" {
		saltBytes = []byte(salt)
	}

	encoding := "base32"
	encryption := "standard"
	passwordHash := ""
	for i, opt := range opts {
		switch i {
		case 0:
			if opt != "" {
				encoding = opt
			}
		case 1:
			if opt != "" {
				encryption = opt
			}
		case 2:
			if opt != "" {
				passwordHash = opt
			}
		}
	}

	if passwordHash == PasswordHashArgon2id {
		password = argon2idStretch(password, salt)
	}

	key, err := scrypt.Key([]byte(password), saltBytes, 16384, 8, 1, 80)
	if err != nil {
		return nil, err
	}

	c := &RcloneCipher{}
	copy(c.dataKey[:], key[0:32])
	copy(c.nameKey[:], key[32:64])
	copy(c.nameTweak[:], key[64:80])
	c.filenameEncoding = encoding
	c.filenameEncryption = encryption
	return c, nil
}

func (c *RcloneCipher) DecryptBlock(ciphertext []byte, blockIndex uint64, fileNonce [FileNonceSize]byte) ([]byte, error) {
	var nonce [FileNonceSize]byte
	copy(nonce[:], fileNonce[:])
	u := blockIndex
	for i := 0; i < 8 && u > 0; i++ {
		u += uint64(nonce[i])
		nonce[i] = byte(u)
		u >>= 8
	}

	plaintext, ok := secretbox.Open(nil, ciphertext, &nonce, &c.dataKey)
	if !ok {
		return nil, errors.New("failed to authenticate decrypted block")
	}

	return plaintext, nil
}

func (c *RcloneCipher) EncryptBlock(plaintext []byte, blockIndex uint64, fileNonce [FileNonceSize]byte) ([]byte, error) {
	var nonce [FileNonceSize]byte
	copy(nonce[:], fileNonce[:])
	u := blockIndex
	for i := 0; i < 8 && u > 0; i++ {
		u += uint64(nonce[i])
		nonce[i] = byte(u)
		u >>= 8
	}

	ciphertext := secretbox.Seal(nil, plaintext, &nonce, &c.dataKey)
	return ciphertext, nil
}

func (c *RcloneCipher) GenerateRandomNonce() ([FileNonceSize]byte, error) {
	var nonce [FileNonceSize]byte
	_, err := io.ReadFull(rand.Reader, nonce[:])
	return nonce, err
}

func (c *RcloneCipher) EncryptSegment(plaintext string) string {
	if plaintext == "" {
		return ""
	}

	switch c.filenameEncryption {
	case "off":
		return plaintext
	case "obfuscate":
		return c.obfuscateSegment(plaintext)
	default:
		return c.encryptSegmentStandard(plaintext)
	}
}

func (c *RcloneCipher) encryptSegmentStandard(plaintext string) string {
	plaintextBytes := []byte(plaintext)
	paddingLen := 16 - (len(plaintextBytes) % 16)
	for range paddingLen {
		plaintextBytes = append(plaintextBytes, byte(paddingLen))
	}

	block, _ := aes.NewCipher(c.nameKey[:])
	ciphertext := eme.Transform(block, c.nameTweak[:], plaintextBytes, eme.DirectionEncrypt)

	switch c.filenameEncoding {
	case "base64":
		return rcloneBase64.EncodeToString(ciphertext)
	default:
		return strings.ToLower(rcloneBase32.EncodeToString(ciphertext))
	}
}

func (c *RcloneCipher) DecryptSegment(encrypted string) (string, error) {
	if encrypted == "" {
		return "", nil
	}

	switch c.filenameEncryption {
	case "off":
		return encrypted, nil
	case "obfuscate":
		return c.deobfuscateSegment(encrypted)
	default:
		return c.decryptSegmentStandard(encrypted)
	}
}

func (c *RcloneCipher) decryptSegmentStandard(encrypted string) (string, error) {
	for _, enc := range []string{c.filenameEncoding, otherEncoding(c.filenameEncoding)} {
		plain, err := c.decodeAndDecrypt(encrypted, enc)
		if err == nil {
			return plain, nil
		}
	}

	return "", errors.New("failed to decrypt filename")
}

func otherEncoding(enc string) string {
	if enc == "base64" {
		return "base32"
	}
	return "base64"
}

func (c *RcloneCipher) decodeAndDecrypt(encrypted, encoding string) (string, error) {
	if encrypted == "" {
		return "", nil
	}

	var rawCiphertext []byte
	var err error
	switch encoding {
	case "base64":
		rawCiphertext, err = rcloneBase64.DecodeString(encrypted)
	default:
		rawCiphertext, err = rcloneBase32.DecodeString(strings.ToUpper(encrypted))
	}
	if err != nil {
		return "", err
	}

	if len(rawCiphertext) == 0 {
		return "", errors.New("empty ciphertext")
	}
	if len(rawCiphertext)%16 != 0 {
		return "", errors.New("ciphertext length is not a multiple of 16")
	}

	block, _ := aes.NewCipher(c.nameKey[:])
	plaintextBytes := eme.Transform(block, c.nameTweak[:], rawCiphertext, eme.DirectionDecrypt)

	if len(plaintextBytes) == 0 {
		return "", nil
	}
	paddingLen := int(plaintextBytes[len(plaintextBytes)-1])
	if paddingLen > 0 && paddingLen <= 16 {
		plaintextBytes = plaintextBytes[:len(plaintextBytes)-paddingLen]
	}

	return string(plaintextBytes), nil
}

func (c *RcloneCipher) EncryptedSize(size int64) int64 {
	blocks := size / BlockDataSize
	residue := size % BlockDataSize
	encSize := int64(FileHeaderSize) + blocks*(BlockHeaderSize+BlockDataSize)
	if residue != 0 {
		encSize += BlockHeaderSize + residue
	}
	return encSize
}

func (c *RcloneCipher) DecryptedSize(size int64) (int64, error) {
	if size <= 0 {
		return 0, nil
	}
	size -= int64(FileHeaderSize)
	if size < 0 {
		return 0, errors.New("file too short")
	}
	blocks := size / BlockSize
	residue := size % BlockSize
	decSize := blocks * BlockDataSize
	if residue != 0 {
		residue -= BlockHeaderSize
		if residue <= 0 {
			return 0, errors.New("bad block header")
		}
		decSize += residue
	}
	return decSize, nil
}

func (c *RcloneCipher) obfuscateSegment(plaintext string) string {
	if plaintext == "" {
		return ""
	}
	if !utf8.ValidString(plaintext) {
		return "!." + plaintext
	}

	var dir int
	for _, runeValue := range plaintext {
		dir += int(runeValue)
	}
	dir %= 256

	var result bytes.Buffer
	result.WriteString(strconv.Itoa(dir))
	result.WriteByte('.')
	for i := range len(c.nameKey) {
		dir += int(c.nameKey[i])
	}

	for _, runeValue := range plaintext {
		switch {
		case runeValue == obfuscQuoteRune:
			result.WriteRune(obfuscQuoteRune)
			result.WriteRune(obfuscQuoteRune)

		case runeValue >= '0' && runeValue <= '9':
			thisdir := (dir % 9) + 1
			newRune := '0' + (int(runeValue)-'0'+thisdir)%10
			result.WriteRune(rune(newRune))

		case (runeValue >= 'A' && runeValue <= 'Z') ||
			(runeValue >= 'a' && runeValue <= 'z'):
			thisdir := dir%25 + 1
			pos := int(runeValue - 'A')
			if pos >= 26 {
				pos -= 6
			}
			pos = (pos + thisdir) % 52
			if pos >= 26 {
				pos += 6
			}
			result.WriteRune(rune('A' + pos))

		case runeValue >= 0xA0 && runeValue <= 0xFF:
			thisdir := (dir % 95) + 1
			newRune := 0xA0 + (int(runeValue)-0xA0+thisdir)%96
			result.WriteRune(rune(newRune))

		case runeValue >= 0x100:
			thisdir := (dir % 127) + 1
			base := int(runeValue - runeValue%256)
			newRune := rune(base + (int(runeValue)-base+thisdir)%256)
			if !utf8.ValidRune(newRune) {
				result.WriteRune(obfuscQuoteRune)
				result.WriteRune(runeValue)
			} else {
				result.WriteRune(newRune)
			}

		default:
			result.WriteRune(runeValue)
		}
	}
	return result.String()
}

func (c *RcloneCipher) deobfuscateSegment(ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}
	before, after, ok := strings.Cut(ciphertext, ".")
	if !ok {
		return "", errors.New("not an obfuscated file")
	}
	num := before
	if num == "!" {
		return after, nil
	}
	dir, err := strconv.Atoi(num)
	if err != nil {
		return "", errors.New("not an obfuscated file")
	}
	for i := range len(c.nameKey) {
		dir += int(c.nameKey[i])
	}

	var result bytes.Buffer
	inQuote := false
	for _, runeValue := range after {
		if inQuote {
			result.WriteRune(runeValue)
			inQuote = false
			continue
		}
		if runeValue == obfuscQuoteRune {
			inQuote = true
			continue
		}
		switch {
		case runeValue >= '0' && runeValue <= '9':
			thisdir := (dir % 9) + 1
			orig := (int(runeValue) - '0' - thisdir) % 10
			if orig < 0 {
				orig += 10
			}
			result.WriteRune(rune('0' + orig))

		case (runeValue >= 'A' && runeValue <= 'Z') ||
			(runeValue >= 'a' && runeValue <= 'z'):
			thisdir := dir%25 + 1
			pos := int(runeValue - 'A')
			if pos >= 26 {
				pos -= 6
			}
			pos = (pos - thisdir) % 52
			if pos < 0 {
				pos += 52
			}
			if pos >= 26 {
				pos += 6
			}
			result.WriteRune(rune('A' + pos))

		case runeValue >= 0xA0 && runeValue <= 0xFF:
			thisdir := (dir % 95) + 1
			orig := (int(runeValue) - 0xA0 - thisdir) % 96
			if orig < 0 {
				orig += 96
			}
			result.WriteRune(rune(0xA0 + orig))

		case runeValue >= 0x100:
			thisdir := (dir % 127) + 1
			base := int(runeValue - runeValue%256)
			orig := (int(runeValue) - base - thisdir) % 256
			if orig < 0 {
				orig += 256
			}
			result.WriteRune(rune(base + orig))

		default:
			result.WriteRune(runeValue)
		}
	}
	return result.String(), nil
}




