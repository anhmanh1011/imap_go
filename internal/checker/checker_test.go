package checker

import (
	"testing"
)

func TestParseLine(t *testing.T) {
	tests := []struct {
		line       string
		wantUser   string
		wantPass   string
		wantDomain string
		wantOK     bool
	}{
		{"user@gmail.com:secret", "user@gmail.com", "secret", "gmail.com", true},
		{"user@example.com:p:a:s:s", "user@example.com", "p:a:s:s", "example.com", true},
		{"user@domain.com:", "user@domain.com", "", "domain.com", true},
		{"noatsign:pass", "", "", "", false},
		{":pass", "", "", "", false},
		{"", "", "", "", false},
	}
	for _, tt := range tests {
		user, pass, domain, ok := parseLine(tt.line)
		if ok != tt.wantOK {
			t.Errorf("parseLine(%q): ok=%v want %v", tt.line, ok, tt.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if user != tt.wantUser || pass != tt.wantPass || domain != tt.wantDomain {
			t.Errorf("parseLine(%q) = (%q,%q,%q), want (%q,%q,%q)",
				tt.line, user, pass, domain, tt.wantUser, tt.wantPass, tt.wantDomain)
		}
	}
}

func TestUniqueDomains(t *testing.T) {
	creds := []Credential{
		{Domain: "gmail.com"},
		{Domain: "gmail.com"},
		{Domain: "yahoo.com"},
	}
	got := UniqueDomains(creds)
	if len(got) != 2 {
		t.Errorf("len=%d, want 2", len(got))
	}
}
