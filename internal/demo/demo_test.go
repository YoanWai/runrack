package demo

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite golden files")

// The --json schema only gains fields within v1: this golden must never lose one.
func TestSnapshotJSONGolden(t *testing.T) {
	now := time.Date(2026, 11, 23, 17, 42, 7, 0, time.UTC)
	got, err := json.MarshalIndent(World(now, now), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	const path = "testdata/snapshot.json"
	if *update {
		if err := os.WriteFile(path, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(want), got) {
		t.Fatalf("snapshot JSON changed:\n%s", got)
	}
	if !bytes.Contains(got, []byte(`"schema": "runrack.snapshot/v1"`)) || !bytes.Contains(got, []byte(`"takenAt": "2026-11-23T17:42:07Z"`)) {
		t.Fatal("schema tag or UTC RFC 3339 timestamps missing")
	}
}
