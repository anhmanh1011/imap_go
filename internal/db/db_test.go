package db

import (
	"os"
	"testing"
)

func TestDomainKey(t *testing.T) {
	tests := []struct {
		domain string
		want   int64
	}{
		{"gmail.com", 2691187859986816277},
		{"outlook.com", -4558591710954502866},
		{"hotmail.com", -6687126143800646354},
		{"yahoo.com", 8509464350704277843},
		{"  Gmail.COM  ", 2691187859986816277}, // whitespace + case normalization
		{"gmail.com.", 2691187859986816277},    // trailing dot stripped
	}
	for _, tt := range tests {
		got := domainKey(tt.domain)
		if got != tt.want {
			t.Errorf("domainKey(%q) = %d, want %d", tt.domain, got, tt.want)
		}
	}
}

func TestBatchLookup_Integration(t *testing.T) {
	const dbPath = "/root/imap_checker/Servers.db"
	if _, err := os.Stat(dbPath); err != nil {
		t.Skipf("Servers.db not found at %s, skipping", dbPath)
	}

	domains := []string{"gmail.com", "outlook.com", "no-such-domain-xyz.invalid"}
	got, err := BatchLookup(dbPath, domains)
	if err != nil {
		t.Fatalf("BatchLookup: %v", err)
	}

	if got["gmail.com"].Host != "imap.gmail.com" {
		t.Errorf("gmail host = %q, want imap.gmail.com", got["gmail.com"].Host)
	}
	if got["gmail.com"].Port != 993 {
		t.Errorf("gmail port = %d, want 993", got["gmail.com"].Port)
	}
	if got["outlook.com"].Host != "outlook.office365.com" {
		t.Errorf("outlook host = %q, want outlook.office365.com", got["outlook.com"].Host)
	}
	if !got["no-such-domain-xyz.invalid"].Fallback {
		t.Error("unknown domain should have Fallback=true")
	}
	if got["no-such-domain-xyz.invalid"].Host != "imap.no-such-domain-xyz.invalid" {
		t.Errorf("fallback host = %q", got["no-such-domain-xyz.invalid"].Host)
	}
}
