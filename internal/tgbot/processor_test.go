package tgbot

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"imap_checker/internal/proxy"
)

func TestCountFileLines(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("a\nb\n\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := countFileLines(p)
	if err != nil {
		t.Fatalf("countFileLines: %v", err)
	}
	if n != 3 { // blank line ignored
		t.Fatalf("countFileLines = %d, want 3", n)
	}
}

func TestCountFileLinesMissing(t *testing.T) {
	n, err := countFileLines(filepath.Join(t.TempDir(), "nope.txt"))
	if err != nil {
		t.Fatalf("missing file should be 0/nil, got err %v", err)
	}
	if n != 0 {
		t.Fatalf("missing file count = %d, want 0", n)
	}
}

// TestProcessSmoke exercises the full pipeline against a domain that cannot
// resolve, so no real IMAP server is contacted (DNS fails fast).
func TestProcessSmoke(t *testing.T) {
	if _, err := os.Stat("../../Servers.db"); err != nil {
		t.Skip("Servers.db not present; skipping smoke test")
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "input.txt")
	if err := os.WriteFile(in, []byte("user@nx-no-such-domain.invalid:pw\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")

	pool, _ := proxy.LoadFile("") // empty pool → direct dial
	res, err := Process(context.Background(), 2, "../../Servers.db", pool, in, out)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Total != 1 {
		t.Errorf("Total = %d, want 1", res.Total)
	}
	if res.Valid != 0 {
		t.Errorf("Valid = %d, want 0", res.Valid)
	}
	if _, err := os.Stat(res.ValidTxtPath); err != nil {
		t.Errorf("valid.txt not created: %v", err)
	}
}
