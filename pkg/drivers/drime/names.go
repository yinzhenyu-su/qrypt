package drime

import (
	"context"
	"fmt"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// Drime silently trims leading/trailing spaces, drops backslashes, and
// truncates names past 255 bytes. qrypt must preserve the exact local name, so
// such names are stored under a reversible "qrypt~" escape prefix instead of
// being rejected. Names longer than 255 bytes cannot be escaped within the
// provider limit and are rejected.
const remoteNamePrefix = "qrypt~"

func decodeRemoteName(remote string) string {
	if !strings.HasPrefix(remote, remoteNamePrefix) {
		return remote
	}
	raw := strings.TrimPrefix(remote, remoteNamePrefix)
	var b strings.Builder
	b.Grow(len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] == '%' && i+2 < len(raw) {
			if hi, ok := fromHex(raw[i+1]); ok {
				if lo, ok := fromHex(raw[i+2]); ok {
					b.WriteByte(hi<<4 | lo)
					i += 2
					continue
				}
			}
		}
		b.WriteByte(raw[i])
	}
	return b.String()
}

func encodeRemoteName(plain string) (string, error) {
	if plain == "" {
		return "", fmt.Errorf("drime: empty name: %w", drive.ErrInvalidInput)
	}
	if strings.ContainsRune(plain, '/') {
		return "", fmt.Errorf("drime: name contains '/': %w", drive.ErrInvalidInput)
	}
	if strings.ContainsRune(plain, '\x00') {
		return "", fmt.Errorf("drime: name contains NUL: %w", drive.ErrInvalidInput)
	}
	if len(plain) > 255 {
		return "", fmt.Errorf("drime: name is %d bytes, maximum is 255: %w", len(plain), drive.ErrInvalidInput)
	}
	if !needsRemoteEscape(plain) {
		return plain, nil
	}
	var b strings.Builder
	b.WriteString(remoteNamePrefix)
	for i := 0; i < len(plain); i++ {
		ch := plain[i]
		if isRemotePlainByte(ch) {
			b.WriteByte(ch)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", ch)
	}
	if b.Len() > 255 {
		return "", fmt.Errorf("drime: escaped name is %d bytes, maximum is 255: %w", b.Len(), drive.ErrInvalidInput)
	}
	return b.String(), nil
}

func needsRemoteEscape(name string) bool {
	if strings.HasPrefix(name, remoteNamePrefix) {
		return true
	}
	if strings.TrimSpace(name) != name {
		return true
	}
	if strings.ContainsRune(name, '\\') {
		return true
	}
	for i := 0; i < len(name); i++ {
		if name[i] < 0x20 || name[i] == 0x7f {
			return true
		}
	}
	return false
}

func isRemotePlainByte(ch byte) bool {
	return ch >= 'A' && ch <= 'Z' ||
		ch >= 'a' && ch <= 'z' ||
		ch >= '0' && ch <= '9' ||
		ch == '.' || ch == '_' || ch == '-'
}

func fromHex(ch byte) (byte, bool) {
	switch {
	case ch >= '0' && ch <= '9':
		return ch - '0', true
	case ch >= 'a' && ch <= 'f':
		return ch - 'a' + 10, true
	case ch >= 'A' && ch <= 'F':
		return ch - 'A' + 10, true
	default:
		return 0, false
	}
}

// Encode implements drive.RemoteNameCodec with Drime's reversible escape.
func (d *Driver) Encode(plain string) (string, error) {
	return encodeRemoteName(plain)
}

// Decode maps a provider name back to the plain name. ok is false when the
// remote name was not escaped.
func (d *Driver) Decode(remote string) (string, bool) {
	plain := decodeRemoteName(remote)
	return plain, plain != remote
}

func (d *Driver) ResolveRemoteName(_ context.Context, plainName string) (drive.RemoteNameInfo, error) {
	remote, err := encodeRemoteName(plainName)
	if err != nil {
		return drive.RemoteNameInfo{}, err
	}
	return drive.RemoteNameInfo{PlainName: plainName, RemoteName: remote}, nil
}
