package crypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
)

var rcloneObscureKey = []byte{
	0x9c, 0x93, 0x5b, 0x48, 0x73, 0x0a, 0x55, 0x4d,
	0x6b, 0xfd, 0x7c, 0x63, 0xc8, 0x86, 0xa9, 0x2b,
	0xd3, 0x90, 0x19, 0x8e, 0xb8, 0x12, 0x8a, 0xfb,
	0xf4, 0xde, 0x16, 0x2b, 0x8b, 0x95, 0xf6, 0x38,
}

// RevealRcloneConfigValue reveals a value produced by rclone obscure.
func RevealRcloneConfigValue(value string) (string, error) {
	ciphertext, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", fmt.Errorf("base64 decode failed: %w", err)
	}
	if len(ciphertext) < aes.BlockSize {
		return "", errors.New("input too short")
	}
	plain := ciphertext[aes.BlockSize:]
	if err := rcloneObscureCrypt(plain, plain, ciphertext[:aes.BlockSize]); err != nil {
		return "", err
	}
	return string(plain), nil
}

// ObscureRcloneConfigValue obscures a value using rclone's config format.
func ObscureRcloneConfigValue(value string) (string, error) {
	plaintext := []byte(value)
	if math.MaxInt32-aes.BlockSize < len(plaintext) {
		return "", fmt.Errorf("value too large")
	}
	ciphertext := make([]byte, aes.BlockSize+len(plaintext))
	iv := ciphertext[:aes.BlockSize]
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return "", fmt.Errorf("read iv: %w", err)
	}
	if err := rcloneObscureCrypt(ciphertext[aes.BlockSize:], plaintext, iv); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

func rcloneObscureCrypt(out, in, iv []byte) error {
	block, err := aes.NewCipher(rcloneObscureKey)
	if err != nil {
		return err
	}
	stream := cipher.NewCTR(block, iv)
	stream.XORKeyStream(out, in)
	return nil
}
