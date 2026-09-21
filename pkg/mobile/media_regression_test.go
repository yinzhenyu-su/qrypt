package mobile

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/util"
)

// openMobileMediaCore opens a core over a quark mount rooted at remote and
// registers session close.
func openMobileMediaCore(t *testing.T, remote string) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
[[mounts]]
name = "quark"
type = "localfs"
[mounts.params]
root_path = `+util.TOMLPath(remote)+`
`), 0o644); err != nil {
		t.Fatal(err)
	}
	coreID, err := openCore(configPath, testRuntimeJSON(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeCore(coreID) })
	return coreID
}

func TestMobileProbeMP4JSONSchemaAndValues(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := mobileRawMP4()
	if err := os.WriteFile(filepath.Join(remote, "movie.mp4"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	coreID := openMobileMediaCore(t, remote)

	rawJSON := ProbeMP4JSON(coreID, "/quark/movie.mp4", 0)
	var out struct {
		OK   bool `json:"ok"`
		Data struct {
			IsMP4          bool  `json:"is_mp4"`
			FastStart      bool  `json:"fast_start"`
			NeedsFastStart bool  `json:"needs_fast_start"`
			FtypOffset     int64 `json:"ftyp_offset,omitempty"`
			FtypSize       int64 `json:"ftyp_size,omitempty"`
			MoovOffset     int64 `json:"moov_offset,omitempty"`
			MoovSize       int64 `json:"moov_size,omitempty"`
			MdatOffset     int64 `json:"mdat_offset,omitempty"`
			MdatSize       int64 `json:"mdat_size,omitempty"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK {
		t.Fatalf("ProbeMP4JSON = %s, want ok", rawJSON)
	}
	d := out.Data
	if !d.IsMP4 || d.FastStart || !d.NeedsFastStart {
		t.Fatalf("probe flags = %+v, want is_mp4 + needs_fast_start", d)
	}
	if d.FtypOffset != 0 || d.FtypSize != 12 || d.MdatOffset != 12 || d.MdatSize != 15 ||
		d.MoovOffset != 27 || d.MoovSize != 32 {
		t.Fatalf("probe atoms = %+v, want ftyp(0,12) mdat(12,15) moov(27,32)", d)
	}
	// Pin every field that renders in this fixture: all non-omitempty fields
	// (including fast_start:false, which is explicitly tagged without
	// omitempty) plus the non-zero omitempty fields. A schema drift in
	// either direction (renamed or dropped tag) fails the regression.
	for _, key := range []string{"is_mp4", "fast_start", "needs_fast_start", "ftyp_size", "moov_offset", "moov_size", "mdat_offset", "mdat_size"} {
		if !strings.Contains(rawJSON, `"`+key+`"`) {
			t.Fatalf("ProbeMP4JSON JSON missing key %q: %s", key, rawJSON)
		}
	}
}

func TestMobileOpenVirtualFileJSONShape(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := mobileRawMP4()
	if err := os.WriteFile(filepath.Join(remote, "movie.mp4"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "plain.bin"), []byte("plain bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	coreID := openMobileMediaCore(t, remote)

	// Passthrough open: no mp4 object in the info payload at all.
	passthrough := OpenVirtualFileJSON(coreID, "/quark/plain.bin", "passthrough", 0)
	var pt struct {
		OK   bool              `json:"ok"`
		Data virtualOpenResult `json:"data"`
	}
	if err := json.Unmarshal([]byte(passthrough), &pt); err != nil {
		t.Fatal(err)
	}
	if !pt.OK || pt.Data.Handle == "" {
		t.Fatalf("passthrough open = %s, want handle", passthrough)
	}
	defer CloseVirtualFileJSON(pt.Data.Handle)
	if pt.Data.Info.Mode != "passthrough" || pt.Data.Info.Transformed || pt.Data.Info.MP4 != nil || pt.Data.Info.Size != 11 {
		t.Fatalf("passthrough info = %+v, want mode passthrough size 11 without mp4", pt.Data.Info)
	}
	if strings.Contains(passthrough, `"mp4"`) {
		t.Fatalf("passthrough open JSON unexpectedly contains mp4: %s", passthrough)
	}

	// Auto-media open of a file needing faststart: transformed with mp4 info.
	auto := OpenVirtualFileJSON(coreID, "/quark/movie.mp4", "auto_media", 0)
	var am struct {
		OK   bool              `json:"ok"`
		Data virtualOpenResult `json:"data"`
	}
	if err := json.Unmarshal([]byte(auto), &am); err != nil {
		t.Fatal(err)
	}
	if !am.OK || am.Data.Handle == "" {
		t.Fatalf("auto open = %s, want handle", auto)
	}
	defer CloseVirtualFileJSON(am.Data.Handle)
	if am.Data.Info.Mode != "mp4_faststart" || !am.Data.Info.Transformed || am.Data.Info.Size != int64(len(raw)) {
		t.Fatalf("auto info = %+v, want transformed mp4_faststart of size %d", am.Data.Info, len(raw))
	}
	if am.Data.Info.MP4 == nil || !am.Data.Info.MP4.NeedsFastStart {
		t.Fatalf("auto info.MP4 = %+v, want needs_fast_start probe", am.Data.Info.MP4)
	}
	if !strings.Contains(auto, `"mp4"`) || !strings.Contains(auto, `"needs_fast_start":true`) {
		t.Fatalf("auto open JSON missing mp4 details: %s", auto)
	}
}

func TestMobileVirtualFileReadAndCloseRegression(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("virtual video data")
	if err := os.WriteFile(filepath.Join(remote, "video.bin"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	coreID := openMobileMediaCore(t, remote)

	raw := OpenVirtualFileJSON(coreID, "/quark/video.bin", "passthrough", 0)
	var opened struct {
		OK   bool              `json:"ok"`
		Data virtualOpenResult `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &opened); err != nil {
		t.Fatal(err)
	}
	if !opened.OK || opened.Data.Handle == "" {
		t.Fatalf("OpenVirtualFileJSON = %s, want handle", raw)
	}
	handleID := opened.Data.Handle

	buf := make([]byte, len(content))
	n, err := ReadVirtualFileAtInto(handleID, 0, buf, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(content) || string(buf[:n]) != string(content) {
		t.Fatalf("ReadVirtualFileAtInto n=%d data=%q, want %q", n, buf[:n], content)
	}

	// Empty destination short-circuits before handle lookup: preserved.
	if n, err := ReadVirtualFileAtInto("no-such-handle", 0, nil, 0); n != 0 || err != nil {
		t.Fatalf("ReadVirtualFileAtInto empty dst = %d,%v, want 0,nil", n, err)
	}
	// Unknown handle with a real buffer fails through the error envelope.
	if n, err := ReadVirtualFileAtInto("no-such-handle", 0, buf, 0); err == nil {
		t.Fatalf("ReadVirtualFileAtInto unknown handle n=%d err=%v, want error", n, err)
	}

	// A handle closes exactly once: the first Close succeeds, and a second
	// Close of the same handle is rejected as unknown because the registry
	// entry was removed by the first close.
	first := CloseVirtualFileJSON(handleID)
	var closed struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(first), &closed); err != nil {
		t.Fatal(err)
	}
	if !closed.OK {
		t.Fatalf("CloseVirtualFileJSON = %s, want ok", first)
	}
	if second := CloseVirtualFileJSON(handleID); strings.Contains(second, `"ok":true`) {
		t.Fatalf("second CloseVirtualFileJSON = %s, want unknown handle error", second)
	}
}

// mobileRawMP4 builds ftyp+mdat+moov where moov carries a co64 chunk table:
// ftyp(0,12) mdat(12,15) moov(27,32), so moov needs to move ahead of mdat.
func mobileRawMP4() []byte {
	co64 := make([]byte, 24)
	binary.BigEndian.PutUint32(co64[0:4], uint32(len(co64)))
	copy(co64[4:8], "co64")
	binary.BigEndian.PutUint32(co64[12:16], 1)
	binary.BigEndian.PutUint64(co64[16:24], 16)
	ftyp := mobileAtomBytes("ftyp", []byte("isom"))
	mdat := mobileAtomBytes("mdat", []byte("payload"))
	moov := mobileAtomBytes("moov", co64)
	return append(ftyp, append(mdat, moov...)...)
}

func mobileAtomBytes(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}
