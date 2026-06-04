package checker

import "strings"

// blockedDomains is a set of mail domains known to not support IMAP or to be
// so hostile to automated login attempts (deliberate fake-OK, port-blocking,
// etc.) that attempting them wastes worker time with zero useful signal.
//
// Domain matching is exact OR suffix — listing "int.pl" also blocks
// "szkola.int.pl", "games.int.pl", etc.
//
// To add a new domain: append it to the slice below and rebuild.
var blockedDomains = func() map[string]struct{} {
	list := []string{
		// Interia Group (Poland) — fake LOGIN OK anti-scraping countermeasure;
		// SELECT INBOX always fails for real addresses too; no real IMAP value.
		"interia.pl",
		"interia.eu",
		"interia.com",
		"int.pl",
		"poczta.fm",
		"pisz.to",
		"pacz.to",
		"intmail.pl",
		"interiowy.pl",
		"adresik.net",
		"ogarnij.se",
		"vip.interia.pl",
	}
	m := make(map[string]struct{}, len(list))
	for _, d := range list {
		m[d] = struct{}{}
	}
	return m
}()

// IsBlocked reports whether domain is on the blocklist.
// Matching is case-insensitive; subdomain suffixes of a blocked entry are also blocked.
func IsBlocked(domain string) bool {
	d := strings.ToLower(strings.TrimSpace(domain))
	if _, ok := blockedDomains[d]; ok {
		return true
	}
	// Check suffix match so e.g. "szkola.int.pl" is caught by "int.pl".
	for blocked := range blockedDomains {
		if strings.HasSuffix(d, "."+blocked) {
			return true
		}
	}
	return false
}

// FilterBlocked removes credentials whose domain is on the blocklist.
// Returns the kept slice and the number of entries removed.
func FilterBlocked(creds []Credential) ([]Credential, int) {
	kept := creds[:0:len(creds)]
	skipped := 0
	for _, c := range creds {
		if IsBlocked(c.Domain) {
			skipped++
		} else {
			kept = append(kept, c)
		}
	}
	return kept, skipped
}
