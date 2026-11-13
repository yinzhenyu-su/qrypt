package upload

import (
	"reflect"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// TestVerifyIdentityFieldListsAgree is a verification harness, not a product
// test: the identity of a pending record is a hand-maintained field list that
// exists twice (store.sameUploadRecord and SameUploadRecord). This walk over
// PendingUpload reports which fields the two lists agree on, so drift between
// them would show up as a disagreement rather than as a silent change in what
// "the same generation" means.
func TestVerifyIdentityFieldListsAgree(t *testing.T) {
	base := PendingUpload{
		Path:          "/a.txt",
		FID:           "fid",
		ParentID:      "parent",
		Name:          "a.txt",
		LocalPath:     "/tmp/a.staging",
		Size:          4,
		ModTime:       5,
		UpdatedAt:     6,
		RetryCount:    7,
		LastError:     "boom",
		PermanentFail: true,
		LastAttemptAt: 8,
		NextAttemptAt: 9,
		ReplaceUpload: &UploadReplacement{ID: "r", ParentID: "parent", Name: "a.tmp", Size: 4},
		SourceHashes:  drive.SourceHashes{drive.HashAlgorithm("sha256"): []byte{1}},
		Staging:       &UploadStagingStatus{Exists: true},
		Frozen:        true,
		UploadStarted: true,
	}

	value := reflect.ValueOf(base)
	typ := value.Type()
	var identity, ignored []string
	for i := 0; i < value.NumField(); i++ {
		other := base
		mutateField(&other, typ.Field(i).Name)
		storeSaysSame := sameUploadRecord(base, other)
		workerSaysSame := SameUploadRecord(base, other)
		if storeSaysSame != workerSaysSame {
			t.Errorf("field %s: store.sameUploadRecord=%v, SameUploadRecord=%v", typ.Field(i).Name, storeSaysSame, workerSaysSame)
			continue
		}
		if storeSaysSame {
			ignored = append(ignored, typ.Field(i).Name)
		} else {
			identity = append(identity, typ.Field(i).Name)
		}
	}
	t.Logf("compared by both lists (%d): %v", len(identity), identity)
	t.Logf("outside the identity (%d): %v", len(ignored), ignored)
	if len(ignored) == 0 {
		t.Fatal("no field is outside the identity; the walk did not vary anything")
	}
}

// mutateField changes exactly one field to a different value.
func mutateField(p *PendingUpload, name string) {
	switch name {
	case "Path":
		p.Path += ".other"
	case "FID":
		p.FID += ".other"
	case "ParentID":
		p.ParentID += ".other"
	case "Name":
		p.Name += ".other"
	case "LocalPath":
		p.LocalPath += ".other"
	case "Size":
		p.Size++
	case "ModTime":
		p.ModTime++
	case "UpdatedAt":
		p.UpdatedAt++
	case "RetryCount":
		p.RetryCount++
	case "LastError":
		p.LastError += "!"
	case "PermanentFail":
		p.PermanentFail = !p.PermanentFail
	case "LastAttemptAt":
		p.LastAttemptAt++
	case "NextAttemptAt":
		p.NextAttemptAt++
	case "ReplaceUpload":
		p.ReplaceUpload = nil
	case "SourceHashes":
		p.SourceHashes[drive.HashAlgorithm("md5")] = []byte{2}
	case "Staging":
		p.Staging = nil
	case "Frozen":
		p.Frozen = !p.Frozen
	case "UploadStarted":
		p.UploadStarted = !p.UploadStarted
	default:
		panic("PendingUpload gained field " + name + "; extend mutateField")
	}
}
