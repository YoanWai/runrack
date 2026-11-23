package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestBadContainerGlobIsRejected(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{"--containers", "runner-["}, &bytes.Buffer{}, &stderr); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), "--containers") {
		t.Fatalf("stderr %q", stderr.String())
	}
	if _, err := parseFlags([]string{"--containers", "runner-*"}, &stderr); err != nil {
		t.Fatal(err)
	}
}
