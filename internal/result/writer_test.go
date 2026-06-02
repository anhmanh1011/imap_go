package result

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriter_RoutesCorrectly(t *testing.T) {
	dir := t.TempDir()

	w, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	w.Write(Result{User: "a@x.com", Pass: "p1", Status: Valid})
	w.Write(Result{User: "b@x.com", Pass: "p2", Status: Invalid})
	w.Write(Result{User: "c@x.com", Pass: "p3", Status: Error, Reason: "timeout"})
	w.Write(Result{User: "d@x.com", Pass: "p4", Status: HostNotFound})

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	cases := []struct{ file, want string }{
		{"valid.txt", "a@x.com:p1\n"},
		{"invalid.txt", "b@x.com:p2\n"},
		{"error.txt", "c@x.com:p3:timeout\n"},
		{"host_not_found.txt", "d@x.com:p4\n"},
	}
	for _, tc := range cases {
		got, err := os.ReadFile(filepath.Join(dir, tc.file))
		if err != nil {
			t.Errorf("%s: read error: %v", tc.file, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("%s: got %q, want %q", tc.file, string(got), tc.want)
		}
	}
}
