// internal/searcher/writer_test.go
package searcher_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"imap_checker/internal/searcher"
)

func TestWriter_Found(t *testing.T) {
	dir := t.TempDir()
	w, err := searcher.NewWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	w.WriteFound("user@example.com", "pass", 3)
	w.WriteNotFound("user2@example.com", "pass2")
	w.WriteError("user3@example.com", "pass3", "login: timeout")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	check := func(name, want string) {
		t.Helper()
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.TrimSpace(string(got)) != want {
			t.Errorf("%s: got %q, want %q", name, string(got), want)
		}
	}
	check("found.txt", "user@example.com:pass:3")
	check("not_found.txt", "user2@example.com:pass2")
	check("error.txt", "user3@example.com:pass3:login: timeout")
}
