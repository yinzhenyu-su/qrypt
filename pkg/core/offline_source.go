package core

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// OfflineInputError is a provider-neutral rejection of an offline request that
// is a usage problem: the caller can fix the input and try again without any
// provider round trip. The CLI maps it to the usage exit code, and no provider
// task is ever created for it.
type OfflineInputError struct {
	Code    string
	Message string
}

func (e *OfflineInputError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// OfflineInputErrorCodes are the stable codes of a rejected offline input.
const (
	OfflineInputSourceRequired  = "offline_source_required"
	OfflineInputSourceScheme    = "offline_source_scheme"
	OfflineInputSourceShareLink = "offline_source_share_link"
	OfflineInputSourceTorrent   = "offline_source_torrent"
)

func offlineInputError(code, format string, args ...any) *OfflineInputError {
	return &OfflineInputError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// NormalizedOfflineSource is the provider-neutral form of a caller's source.
// Source is what qrypt submits to a provider; Identity is what de-duplication
// compares (a torrent file and its equivalent magnet share one Identity).
type NormalizedOfflineSource struct {
	Kind     drive.OfflineSourceKind
	Source   string
	Identity string
}

// NormalizeOfflineSource turns a caller-supplied source into its normalized
// form. Magnet links keep their info hash; a local .torrent file is parsed and
// rewritten as a magnet link (the file itself is never uploaded); HTTP(S) and
// FTP URLs are canonicalized without their fragment. Share links and unknown
// schemes are rejected.
func NormalizeOfflineSource(raw string) (NormalizedOfflineSource, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return NormalizedOfflineSource{}, offlineInputError(OfflineInputSourceRequired, "offline source is required")
	}
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(lower, "magnet:"):
		hash, err := magnetInfoHash(trimmed)
		if err != nil {
			return NormalizedOfflineSource{}, err
		}
		return NormalizedOfflineSource{
			Kind:     drive.OfflineSourceMagnet,
			Source:   trimmed,
			Identity: "magnet:btih:" + hash,
		}, nil
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"), strings.HasPrefix(lower, "ftp://"):
		canonical, identity, err := canonicalizeURLSource(trimmed)
		if err != nil {
			return NormalizedOfflineSource{}, err
		}
		kind := drive.OfflineSourceHTTP
		if strings.HasPrefix(strings.ToLower(canonical), "ftp://") {
			kind = drive.OfflineSourceFTP
		}
		return NormalizedOfflineSource{Kind: kind, Source: canonical, Identity: identity}, nil
	case strings.Contains(trimmed, "://"):
		return NormalizedOfflineSource{}, offlineInputError(OfflineInputSourceScheme, "unsupported offline source scheme in %q", trimmed)
	default:
		return normalizeTorrentFile(trimmed)
	}
}

// normalizeTorrentFile reads a local .torrent file and derives the equivalent
// magnet link from its info hash, name, and trackers. The torrent file is only
// read locally; it is never uploaded.
func normalizeTorrentFile(pathValue string) (NormalizedOfflineSource, error) {
	expanded := expandLocalPath(pathValue)
	data, err := os.ReadFile(expanded)
	if err != nil {
		return NormalizedOfflineSource{}, offlineInputError(OfflineInputSourceTorrent, "read torrent %q: %v", pathValue, err)
	}
	meta, err := parseTorrentMeta(data)
	if err != nil {
		return NormalizedOfflineSource{}, offlineInputError(OfflineInputSourceTorrent, "parse torrent %q: %v", pathValue, err)
	}
	magnet := buildMagnet(meta.infoHash, meta.name, meta.trackers)
	// The torrent file is rewritten as a magnet, so the normalized kind is a
	// magnet: the provider never sees a torrent upload, and the source identity
	// matches the equivalent magnet the user could submit directly.
	return NormalizedOfflineSource{
		Kind:     drive.OfflineSourceMagnet,
		Source:   magnet,
		Identity: "magnet:btih:" + strings.ToLower(meta.infoHash),
	}, nil
}

func expandLocalPath(p string) string {
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), string(filepath.Separator)))
		}
	}
	return p
}

// shareLinkHosts are cloud-drive hosts whose /s/ (or equivalent) paths are
// share links qrypt does not submit as offline sources. A share link points at
// another account's shared item, not at raw content a provider can import.
var shareLinkHosts = map[string]struct{}{
	"pan.quark.cn":        {},
	"drive.quark.cn":      {},
	"www.aliyundrive.com": {},
	"www.alipan.com":      {},
	"115.com":             {},
	"anxia.com":           {},
}

func canonicalizeURLSource(raw string) (canonical, identity string, err error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", "", offlineInputError(OfflineInputSourceScheme, "invalid source URL %q: %v", raw, err)
	}
	if parsed.Host == "" {
		return "", "", offlineInputError(OfflineInputSourceScheme, "source URL %q has no host", raw)
	}
	if _, isShare := shareLinkHosts[strings.ToLower(parsed.Hostname())]; isShare {
		segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(segments) > 0 && (segments[0] == "s" || segments[0] == "share") {
			return "", "", offlineInputError(OfflineInputSourceShareLink, "share links are not supported as offline sources: %q", raw)
		}
	}
	parsed.Fragment = ""
	canonical = parsed.String()
	return canonical, canonical, nil
}

// magnetInfoHash extracts the lower-case btih info hash from a magnet link.
func magnetInfoHash(magnet string) (string, error) {
	var query string
	if idx := strings.IndexByte(magnet, '?'); idx >= 0 {
		query = magnet[idx+1:]
	} else {
		query = strings.TrimPrefix(magnet, "magnet:")
	}
	for _, part := range strings.Split(query, "&") {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		decodedKey, err := url.QueryUnescape(key)
		if err != nil {
			decodedKey = key
		}
		if !strings.EqualFold(decodedKey, "xt") {
			continue
		}
		decodedValue, err := url.QueryUnescape(value)
		if err != nil {
			decodedValue = value
		}
		lower := strings.ToLower(decodedValue)
		if hash, ok := strings.CutPrefix(lower, "urn:btih:"); ok && hash != "" {
			return hash, nil
		}
	}
	return "", offlineInputError(OfflineInputSourceScheme, "magnet link has no btih info hash: %q", magnet)
}

func buildMagnet(infoHash, name string, trackers []string) string {
	var b strings.Builder
	b.WriteString("magnet:?xt=urn:btih:")
	b.WriteString(strings.ToLower(infoHash))
	if name != "" {
		b.WriteString("&dn=")
		b.WriteString(url.QueryEscape(name))
	}
	seen := map[string]struct{}{}
	for _, tracker := range trackers {
		if tracker == "" {
			continue
		}
		if _, ok := seen[tracker]; ok {
			continue
		}
		seen[tracker] = struct{}{}
		b.WriteString("&tr=")
		b.WriteString(url.QueryEscape(tracker))
	}
	return b.String()
}

// torrentMeta is the subset of torrent metadata qrypt needs to build a magnet
// link from a local .torrent file.
type torrentMeta struct {
	infoHash string
	name     string
	trackers []string
}

// parseTorrentMeta reads a bencoded torrent file, hashing the raw bytes of the
// top-level "info" dictionary (the BitTorrent info hash) and collecting the
// display names and announce trackers.
func parseTorrentMeta(data []byte) (torrentMeta, error) {
	root, rest, err := parseBencode(data)
	if err != nil {
		return torrentMeta{}, err
	}
	if len(rest) != 0 {
		return torrentMeta{}, fmt.Errorf("trailing bytes after torrent dictionary")
	}
	if root.kind != bencodeDict {
		return torrentMeta{}, fmt.Errorf("torrent root is not a dictionary")
	}
	info, ok := root.dict["info"]
	if !ok || info.kind != bencodeDict {
		return torrentMeta{}, fmt.Errorf("torrent has no info dictionary")
	}
	digest := sha1.Sum(data[info.start:info.end])
	meta := torrentMeta{infoHash: hex.EncodeToString(digest[:])}
	if name, ok := info.dict["name"]; ok && name.kind == bencodeString {
		meta.name = string(name.str)
	}
	if announce, ok := root.dict["announce"]; ok && announce.kind == bencodeString {
		meta.trackers = append(meta.trackers, string(announce.str))
	}
	if tiers, ok := root.dict["announce-list"]; ok && tiers.kind == bencodeList {
		for _, tier := range tiers.list {
			if tier.kind != bencodeList {
				continue
			}
			for _, tracker := range tier.list {
				if tracker.kind == bencodeString {
					meta.trackers = append(meta.trackers, string(tracker.str))
				}
			}
		}
	}
	return meta, nil
}

type bencodeKind uint8

const (
	bencodeString bencodeKind = iota
	bencodeInt
	bencodeList
	bencodeDict
)

type bencodeValue struct {
	kind    bencodeKind
	str     []byte
	integer int64
	list    []bencodeValue
	dict    map[string]bencodeValue
	start   int
	end     int
}

// parseBencode parses one bencoded value and returns the remaining bytes. The
// returned value's start/end are absolute offsets into data so a nested
// dictionary (the torrent "info" dict) can be hashed by byte range.
func parseBencode(data []byte) (bencodeValue, []byte, error) {
	return parseBencodeAt(data, 0)
}

func parseBencodeAt(data []byte, base int) (bencodeValue, []byte, error) {
	if len(data) == 0 {
		return bencodeValue{}, nil, fmt.Errorf("empty bencode value")
	}
	switch {
	case data[0] == 'i':
		end := indexByte(data, 'e')
		if end < 0 {
			return bencodeValue{}, nil, fmt.Errorf("unterminated bencode integer")
		}
		var value int64
		if _, err := fmt.Sscanf(string(data[1:end]), "%d", &value); err != nil {
			return bencodeValue{}, nil, fmt.Errorf("invalid bencode integer %q", data[1:end])
		}
		return bencodeValue{kind: bencodeInt, integer: value, start: base, end: base + end + 1}, data[end+1:], nil
	case data[0] == 'l':
		return parseBencodeList(data, base)
	case data[0] == 'd':
		return parseBencodeDict(data, base)
	case data[0] >= '0' && data[0] <= '9':
		return parseBencodeString(data, base)
	default:
		return bencodeValue{}, nil, fmt.Errorf("invalid bencode prefix %q", data[0])
	}
}

func parseBencodeString(data []byte, base int) (bencodeValue, []byte, error) {
	colon := indexByte(data, ':')
	if colon < 0 {
		return bencodeValue{}, nil, fmt.Errorf("unterminated bencode string length")
	}
	var length int
	if _, err := fmt.Sscanf(string(data[:colon]), "%d", &length); err != nil || length < 0 {
		return bencodeValue{}, nil, fmt.Errorf("invalid bencode string length %q", data[:colon])
	}
	if colon+1+length > len(data) {
		return bencodeValue{}, nil, fmt.Errorf("bencode string overruns input")
	}
	value := bencodeValue{kind: bencodeString, str: data[colon+1 : colon+1+length], start: base, end: base + colon + 1 + length}
	return value, data[colon+1+length:], nil
}

func parseBencodeList(data []byte, base int) (bencodeValue, []byte, error) {
	rest := data[1:]
	value := bencodeValue{kind: bencodeList, start: base}
	for {
		if len(rest) == 0 {
			return bencodeValue{}, nil, fmt.Errorf("unterminated bencode list")
		}
		if rest[0] == 'e' {
			value.end = base + (len(data) - len(rest)) + 1
			return value, rest[1:], nil
		}
		offset := base + (len(data) - len(rest))
		item, next, err := parseBencodeAt(rest, offset)
		if err != nil {
			return bencodeValue{}, nil, err
		}
		value.list = append(value.list, item)
		rest = next
	}
}

func parseBencodeDict(data []byte, base int) (bencodeValue, []byte, error) {
	rest := data[1:]
	value := bencodeValue{kind: bencodeDict, dict: map[string]bencodeValue{}, start: base}
	for {
		if len(rest) == 0 {
			return bencodeValue{}, nil, fmt.Errorf("unterminated bencode dictionary")
		}
		if rest[0] == 'e' {
			value.end = base + (len(data) - len(rest)) + 1
			return value, rest[1:], nil
		}
		keyOffset := base + (len(data) - len(rest))
		key, afterKey, err := parseBencodeString(rest, keyOffset)
		if err != nil {
			return bencodeValue{}, nil, err
		}
		valueOffset := keyOffset + (len(rest) - len(afterKey))
		item, next, err := parseBencodeAt(afterKey, valueOffset)
		if err != nil {
			return bencodeValue{}, nil, err
		}
		value.dict[string(key.str)] = item
		rest = next
	}
}

func indexByte(data []byte, b byte) int {
	for i, value := range data {
		if value == b {
			return i
		}
	}
	return -1
}

// offlineFingerprint returns the active de-duplication identity of an offline
// request: the normalized parse mount, source identity, and selection. The
// final destination is deliberately excluded so two fetches of the same source
// share one staging task.
func offlineFingerprint(detail offlineDetail) string {
	parseDir := detail.ParseDir
	if strings.TrimSpace(parseDir) != "" {
		parseDir = vfstypes.CleanVirtualPath(parseDir)
	}
	parts := []string{
		detail.ParseMount,
		parseDir,
		detail.SourceKind,
		detail.SourceIdentity,
		selectionFingerprint(detail.Selection),
	}
	return strings.Join(parts, "\x00")
}

func selectionFingerprint(selection offlineDetailSelection) string {
	mode := selection.Mode
	if mode == "" {
		mode = "all"
	}
	parts := []string{mode}
	parts = append(parts, fmt.Sprintf("%d", selection.Largest))
	include := append([]string(nil), selection.Include...)
	exclude := append([]string(nil), selection.Exclude...)
	sort.Strings(include)
	sort.Strings(exclude)
	parts = append(parts, include...)
	parts = append(parts, "\x01")
	parts = append(parts, exclude...)
	return strings.Join(parts, "\x00")
}
