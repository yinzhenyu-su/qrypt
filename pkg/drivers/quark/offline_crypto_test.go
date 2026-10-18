package quark

import (
	"errors"
	"testing"
)

// TestWGEncryptGoldenVector pins the wg request encryption against the verified
// client ciphertext recorded in design.md (A.2). It is the request-encryption
// golden test tasks 3.1 and 6.2 require.
func TestWGEncryptGoldenVector(t *testing.T) {
	t.Parallel()

	got, err := wgEncrypt(14100, []byte("hello"), nil)
	if err != nil {
		t.Fatalf("wgEncrypt: %v", err)
	}
	const want = "NxTFGiL/qySwJSpexPNDuS1r"
	if got != want {
		t.Fatalf("wgEncrypt(14100, hello) = %q, want %q", got, want)
	}
}

// TestWGEncryptRoundTrip verifies a wg-marked response is decodable with the
// same version-keyed key.
func TestWGEncryptRoundTrip(t *testing.T) {
	t.Parallel()

	plain := []byte(`{"url":"magnet:?xt=urn:btih:abc","parse_mode":0}`)
	cipher, err := wgEncrypt(14100, plain, nil)
	if err != nil {
		t.Fatalf("wgEncrypt: %v", err)
	}
	decoded, err := wgDecrypt(cipher, nil)
	if err != nil {
		t.Fatalf("wgDecrypt: %v", err)
	}
	if string(decoded) != string(plain) {
		t.Fatalf("wgDecrypt = %q, want %q", decoded, plain)
	}
}

func TestWGEncryptMissingKey(t *testing.T) {
	t.Parallel()

	if _, err := wgEncrypt(13800, []byte("hello"), nil); !errors.Is(err, ErrQuarkWGKeyMissing) {
		t.Fatalf("wgEncrypt unknown secureNo error = %v, want ErrQuarkWGKeyMissing", err)
	}
}

func TestWGEncryptOverrideKey(t *testing.T) {
	t.Parallel()

	override := map[int]string{13800: "0123456789abcdee"}
	cipher, err := wgEncrypt(13800, []byte("hello"), override)
	if err != nil {
		t.Fatalf("wgEncrypt with override: %v", err)
	}
	decoded, err := wgDecrypt(cipher, override)
	if err != nil {
		t.Fatalf("wgDecrypt with override: %v", err)
	}
	if string(decoded) != "hello" {
		t.Fatalf("override round trip = %q, want hello", decoded)
	}
}
