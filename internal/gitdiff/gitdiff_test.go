package gitdiff

import (
	"testing"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

func TestNULFields(t *testing.T) {
	if got := NULFields(nil); got != nil {
		t.Fatalf("NULFields(nil) = %q", got)
	}
	got := NULFields([]byte("a\x00b c\x00"))
	if len(got) != 2 || got[0] != "a" || got[1] != "b c" {
		t.Fatalf("NULFields = %q", got)
	}
}

func TestStatus(t *testing.T) {
	cases := map[byte]apigen.GitFileChangeStatus{
		'A': apigen.GitFileChangeStatusAdded,
		'D': apigen.GitFileChangeStatusDeleted,
		'R': apigen.GitFileChangeStatusRenamed,
		'U': apigen.GitFileChangeStatusConflicted,
		'X': apigen.GitFileChangeStatusModified,
	}
	for code, want := range cases {
		if got := Status(code); got != want {
			t.Errorf("Status(%q) = %s, want %s", code, got, want)
		}
	}
}

func TestParseChangesRename(t *testing.T) {
	byPath, order := ParseChanges([]byte("R100\x00a.go\x00b.go\x00"), []byte("3\t1\t\x00a.go\x00b.go\x00"))
	if len(order) != 1 || order[0] != "b.go" {
		t.Fatalf("order = %q", order)
	}
	c := byPath["b.go"]
	if c.Status != apigen.GitFileChangeStatusRenamed || c.OldPath == nil || *c.OldPath != "a.go" {
		t.Fatalf("change = %+v", c)
	}
	if c.Additions == nil || *c.Additions != 3 || c.Deletions == nil || *c.Deletions != 1 {
		t.Fatalf("lines = %v %v", c.Additions, c.Deletions)
	}
	if c.Staged != apigen.GitFileChangeStagedNone {
		t.Fatalf("staged = %s", c.Staged)
	}
}

func TestParseChangesMixed(t *testing.T) {
	ns := []byte("M\x00z.txt\x00A\x00img.png\x00D\x00old.txt\x00")
	num := []byte("2\t0\tz.txt\x00-\t-\timg.png\x000\t4\told.txt\x00")
	byPath, order := ParseChanges(ns, num)
	if want := []string{"z.txt", "img.png", "old.txt"}; len(order) != 3 || order[0] != want[0] || order[1] != want[1] || order[2] != want[2] {
		t.Fatalf("order = %q", order)
	}
	if img := byPath["img.png"]; !img.Binary || img.Additions != nil || img.Status != apigen.GitFileChangeStatusAdded {
		t.Fatalf("img = %+v", img)
	}
	if d := byPath["old.txt"]; d.Status != apigen.GitFileChangeStatusDeleted || *d.Deletions != 4 {
		t.Fatalf("old = %+v", d)
	}
}

func TestIsBinaryPatch(t *testing.T) {
	if !IsBinaryPatch([]byte("Binary files a and b differ\n")) {
		t.Fatal("binary not detected")
	}
	if IsBinaryPatch([]byte("@@ -1 +1 @@\n-Binary files a and b differ\n")) {
		t.Fatal("hunk content mistaken for binary")
	}
}

func TestCutTruncated(t *testing.T) {
	if got := string(CutTruncated([]byte("a\nb\npart"))); got != "a\nb\n" {
		t.Fatalf("CutTruncated = %q", got)
	}
	if got := CutTruncated([]byte("no newline")); got != nil {
		t.Fatalf("CutTruncated = %q", got)
	}
}
