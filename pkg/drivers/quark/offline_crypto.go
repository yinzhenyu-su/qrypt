package quark

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Quark offline (wg) request encryption.
//
// The client masks offline request bodies with the "wg" scheme:
//
//	cipher_b64 = base64( [wsgNum 2 bytes big-endian] + AES-128-CBC(PKCS7(plain), key=data_key, iv=data_key) )
//
// where data_key is a per-wsgNum 16-byte constant derived from the client
// binary. Only the production secureNo is reproduced here; other numbers are
// configuration overrides. The goldens and the re-extraction procedure are
// recorded in openspec/changes/add-offline-download/design.md (A.2).
const (
	// quarkWGDefaultSecureNo is the production secureNo the client uses.
	quarkWGDefaultSecureNo = 14100
)

// The client binary embeds the master string
// "d0fa89f19af4fe4c106625c4742fdee0"; only its first 16 bytes are the AES key
// used to unwrap entry keys when re-extracting the per-secureNo data keys
// (search the master string in __TEXT, walk back 5×48B entries, AES-decrypt,
// md5[:16] — see design.md A.2).

// quarkWGKeys is the version-keyed data-key table. 14100 is the production
// secureNo key; other secureNo values can be supplied by configuration.
var quarkWGKeys = map[int]string{
	14100: "e21247a68938d36f",
}

// ErrQuarkWGKeyMissing reports an unknown wsgNum with no configured override.
var ErrQuarkWGKeyMissing = errors.New("quark: no wg data key for secure number")

// wgDataKey resolves the AES-128 key for a wsgNum, preferring a configured
// override.
func wgDataKey(secureNo int, overrides map[int]string) (string, error) {
	if key, ok := overrides[secureNo]; ok && key != "" {
		return key, nil
	}
	if key, ok := quarkWGKeys[secureNo]; ok {
		return key, nil
	}
	return "", fmt.Errorf("%w: %d", ErrQuarkWGKeyMissing, secureNo)
}

// wgEncrypt encrypts a plaintext JSON string for a wsgNum and returns the
// base64 ciphertext the client sends.
func wgEncrypt(secureNo int, plain []byte, overrides map[int]string) (string, error) {
	key, err := wgDataKey(secureNo, overrides)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher([]byte(key))
	if err != nil {
		return "", fmt.Errorf("quark: wg key: %w", err)
	}
	padded := pkcs7Pad(plain, block.BlockSize())
	encrypted := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, []byte(key)).CryptBlocks(encrypted, padded)
	var buffer bytes.Buffer
	var prefix [2]byte
	binary.BigEndian.PutUint16(prefix[:], uint16(secureNo))
	buffer.Write(prefix[:])
	buffer.Write(encrypted)
	return base64.StdEncoding.EncodeToString(buffer.Bytes()), nil
}

// wgDecrypt reverses wgEncrypt for a wg-marked response body.
func wgDecrypt(cipherB64 string, overrides map[int]string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cipherB64))
	if err != nil {
		return nil, fmt.Errorf("quark: wg response base64: %w", err)
	}
	if len(raw) < 2+aes.BlockSize {
		return nil, fmt.Errorf("quark: wg response too short")
	}
	secureNo := int(binary.BigEndian.Uint16(raw[:2]))
	key, err := wgDataKey(secureNo, overrides)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher([]byte(key))
	if err != nil {
		return nil, fmt.Errorf("quark: wg key: %w", err)
	}
	body := raw[2:]
	if len(body)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("quark: wg response is not block aligned")
	}
	plaint := make([]byte, len(body))
	cipher.NewCBCDecrypter(block, []byte(key)).CryptBlocks(plaint, body)
	return pkcs7Unpad(plaint, block.BlockSize())
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - len(data)%blockSize
	return append(append([]byte(nil), data...), bytes.Repeat([]byte{byte(pad)}, pad)...)
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, fmt.Errorf("quark: wg plaintext is not block aligned")
	}
	pad := int(data[len(data)-1])
	if pad == 0 || pad > blockSize || pad > len(data) {
		return nil, fmt.Errorf("quark: wg plaintext padding is invalid")
	}
	for _, b := range data[len(data)-pad:] {
		if int(b) != pad {
			return nil, fmt.Errorf("quark: wg plaintext padding is invalid")
		}
	}
	return data[:len(data)-pad], nil
}

// wgBody marks a request body for wg encryption. It carries the plaintext
// payload so metrics and non-wg callers still see a usable body.
type wgBody struct {
	payload any
	wsgNum  int
}

// encodeBody marshals body, encrypting it when it is a wgBody. It reports
// whether the request must carry the wg marker header.
func encodeBody(body any, overrides map[int]string) (encoded []byte, isWG bool, err error) {
	if body == nil {
		return nil, false, nil
	}
	if wg, ok := body.(wgBody); ok {
		plain, err := json.Marshal(wg.payload)
		if err != nil {
			return nil, false, fmt.Errorf("encode wg body: %w", err)
		}
		secureNo := wg.wsgNum
		if secureNo == 0 {
			secureNo = quarkWGDefaultSecureNo
		}
		cipherText, err := wgEncrypt(secureNo, plain, overrides)
		if err != nil {
			return nil, false, err
		}
		return []byte(cipherText), true, nil
	}
	encoded, err = json.Marshal(body)
	if err != nil {
		return nil, false, fmt.Errorf("encode json body: %w", err)
	}
	return encoded, false, nil
}

// metricBody returns the plaintext payload of a wgBody so request metrics do
// not log ciphertext.
func metricBody(body any) any {
	if wg, ok := body.(wgBody); ok {
		return wg.payload
	}
	return body
}
