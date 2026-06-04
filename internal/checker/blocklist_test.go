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
		// Microsoft — OAuth-only
		{"outlook.com", true},
		{"hotmail.com", true},
		{"live.com", true},
		{"hotmail.co.uk", true},
		{"live.com.br", true},
		// Yahoo/AOL — OAuth-only
		{"yahoo.com", true},
		{"yahoo.co.uk", true},
		{"ymail.com", true},
		{"aol.com", true},
		// No-IMAP providers
		{"protonmail.com", true},
		{"proton.me", true},
		{"tutanota.com", true},
		{"tuta.com", true},
		// Auth-code required
		{"qq.com", true},
		{"163.com", true},
		{"126.com", true},
		{"ukr.net", true},
		// Google — App Password required since May 2022
		{"gmail.com", true},
		{"googlemail.com", true},
		// Should NOT be blocked
		{"rambler.ru", false},
		{"mail.ru", false},
		{"notinteria.pl", false},  // shares suffix but not a subdomain
		{"myint.pl", false},       // "int.pl" suffix but not ".int.pl"
	}
	for _, tc := range cases {
		if got := IsBlocked(tc.domain); got != tc.want {
			t.Errorf("IsBlocked(%q) = %v, want %v", tc.domain, got, tc.want)
		}
	}
}

func TestFilterBlocked(t *testing.T) {
	creds := []Credential{
		{User: "a@rambler.ru", Pass: "p1", Domain: "rambler.ru"},
		{User: "b@interia.pl", Pass: "p2", Domain: "interia.pl"},
		{User: "c@gmail.com", Pass: "p3", Domain: "gmail.com"},
		{User: "d@outlook.com", Pass: "p4", Domain: "outlook.com"},
	}
	kept, skipped := FilterBlocked(creds)
	if skipped != 3 {
		t.Errorf("skipped = %d, want 3", skipped)
	}
	if len(kept) != 1 {
		t.Errorf("len(kept) = %d, want 1", len(kept))
	}
	if kept[0].Domain != "rambler.ru" {
		t.Errorf("unexpected kept domain: %v", kept[0].Domain)
	}
}
