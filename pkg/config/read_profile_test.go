package config

import (
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readPtr[T any](v T) *T { return &v }
func TestReadConfigOmissionAutoZeroAndValidation(t *testing.T) {
	d := drive.NewFakeDriver(drive.FakeWithCapabilities(drive.CapabilityParallelRange))
	for _, tc := range []struct {
		name    string
		c       *ReadConfig
		wantErr bool
	}{
		{"omitted", nil, false}, {"empty", &ReadConfig{}, false},
		{"valid", &ReadConfig{MaxInFlight: readPtr(1), Bulk: &BulkReadConfig{Streams: readPtr(1), Window: readPtr("8MiB"), PartSize: readPtr("auto"), MinParallelSize: readPtr("0")}}, false},
		{"zero budget", &ReadConfig{MaxInFlight: readPtr(0)}, true},
		{"zero streams", &ReadConfig{Bulk: &BulkReadConfig{Streams: readPtr(0)}}, true},
		{"zero window", &ReadConfig{Bulk: &BulkReadConfig{Window: readPtr("0")}}, true},
		{"zero part", &ReadConfig{Bulk: &BulkReadConfig{PartSize: readPtr("0")}}, true},
		{"too large", &ReadConfig{Bulk: &BulkReadConfig{Window: readPtr("33MiB")}}, true},
		{"empty size", &ReadConfig{Bulk: &BulkReadConfig{Window: readPtr("")}}, true},
		{"fraction truncates", &ReadConfig{Bulk: &BulkReadConfig{PartSize: readPtr("0.1")}}, true},
		{"fixed part", &ReadConfig{Bulk: &BulkReadConfig{PartSize: readPtr("512K")}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, err := tc.c.Override()
			if err == nil {
				_, err = drive.ResolveReadPolicy(d, o)
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("override=%+v error=%v", o, err)
			}
		})
	}
	auto, err := (&ReadConfig{Bulk: &BulkReadConfig{PartSize: readPtr("auto"), MinParallelSize: readPtr("0")}}).Override()
	if err != nil {
		t.Fatal(err)
	}
	if auto.PartBytes == nil || *auto.PartBytes != 0 || auto.MinParallelBytes == nil || *auto.MinParallelBytes != 0 {
		t.Fatal("explicit zero semantics lost")
	}
	inherited, _ := (&ReadConfig{}).Override()
	if inherited.PartBytes != nil || inherited.BulkStreams != nil {
		t.Fatal("omission materialized")
	}
}
func TestReadConfigRejectsLegacyTables(t *testing.T) {
	for _, tail := range []string{"[mounts.read_profile]", "[mounts.read_profile]\nstreams=0", "[mounts.read]\nmax_in_flight=1\n[mounts.read_profile]\nstreams=2"} {
		path := filepath.Join(t.TempDir(), "config.toml")
		text := "[[mounts]]\nname=\"test\"\ntype=\"fake\"\n" + tail
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "mounts.read_profile has been removed") {
			t.Fatalf("legacy table accepted: %v", err)
		}
	}
}
func TestReadConfigLoadAndSavePreservesPresence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	text := "[[mounts]]\nname=\"test\"\ntype=\"fake\"\n[mounts.read]\nmax_in_flight=1\n[mounts.read.bulk]\nstreams=2\nwindow=\"8MiB\"\npart_size=\"auto\"\nmin_parallel_size=\"0\"\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	o, err := cfg.Mounts[0].Read.Override()
	if err != nil {
		t.Fatal(err)
	}
	if *o.MaxInFlight != 1 || *o.BulkStreams != 2 || *o.BulkWindowBytes != 8<<20 || *o.PartBytes != 0 {
		t.Fatalf("override=%+v", o)
	}
}
