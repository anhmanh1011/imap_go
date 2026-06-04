package checker

import "testing"

func TestIsBlocked(t *testing.T) {
	cases := []struct {
		domain string
		want   bool
	}{
		{"interia.pl", true},
		{"INTERIA.PL", true},          // case-insensitive
		{"int.pl", true},
		{"szkola.int.pl", true},        // subdomain of blocked entry
		{"games.int.pl", true},
		{"poczta.fm", true},
		{"pisz.to", true},
		{"vip.interia.pl", true},
		{"gmail.com", false},
		{"outlook.com", false},
		{"notinteria.pl", false},       // shares suffix but not a subdomain
		{"myint.pl", false},            // "int.pl" suffix but not ".int.pl"
	}
	for _, tc := range cases {
		if got := IsBlocked(tc.domain); got != tc.want {
			t.Errorf("IsBlocked(%q) = %v, want %v", tc.domain, got, tc.want)
		}
	}
}

func TestFilterBlocked(t *testing.T) {
	creds := []Credential{
		{User: "a@gmail.com", Pass: "p1", Domain: "gmail.com"},
		{User: "b@interia.pl", Pass: "p2", Domain: "interia.pl"},
		{User: "c@szkola.int.pl", Pass: "p3", Domain: "szkola.int.pl"},
		{User: "d@outlook.com", Pass: "p4", Domain: "outlook.com"},
	}
	kept, skipped := FilterBlocked(creds)
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2", skipped)
	}
	if len(kept) != 2 {
		t.Errorf("len(kept) = %d, want 2", len(kept))
	}
	if kept[0].Domain != "gmail.com" || kept[1].Domain != "outlook.com" {
		t.Errorf("unexpected kept domains: %v", kept)
	}
}
