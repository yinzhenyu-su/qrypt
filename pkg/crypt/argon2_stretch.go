package crypt

import (
	"encoding/hex"

	"golang.org/x/crypto/argon2"
)

const (
	// argon2Time is the number of iterations for Argon2id.
	// 3 iterations balances security (resist GPU/ASIC parallelism) with
	// interactive latency (~100ms on modern hardware).
	argon2Time = 3

	// argon2Memory is 64 MiB in KiB — high enough to make GPU cracking
	// impractical (RTX 4090 has 24 GiB, so with 64 MiB per hash ≤384
	// concurrent attempts), low enough to not block startup.
	argon2Memory = 64 * 1024

	// argon2Threads controls Argon2id parallelism. 4 threads is a
	// reasonable default for both desktop and server CPUs.
	argon2Threads = 4

	// argon2KeyLen is the output length in bytes (64 bytes = 128 hex chars).
	// rclone's scrypt expects an alphanumeric password string, so the hex
	// encoding is deliberately long to avoid truncation.
	argon2KeyLen = 64
)

// argon2idStretch applies Argon2id as a pre-stretch before scrypt.
//
// Same (password, salt) always produces the same output — this is a
// deterministic key derivation function, not a random source.
//
// The salt is domain-separated with "qrypt-argon2id:" to prevent
// accidental reuse with other KDF contexts.
func argon2idStretch(password, salt string) string {
	saltBytes := []byte("qrypt-argon2id:" + salt)
	key := argon2.IDKey(
		[]byte(password),
		saltBytes,
		argon2Time,
		argon2Memory,
		argon2Threads,
		argon2KeyLen,
	)
	return hex.EncodeToString(key)
}
