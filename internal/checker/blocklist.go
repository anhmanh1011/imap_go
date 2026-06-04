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
		// ── Interia Group (Poland) ────────────────────────────────────────────────
		// Fake LOGIN OK (~55%) as anti-scraping; SELECT INBOX always fails.
		"interia.pl",
		"interia.eu",
		"interia.com",
		"int.pl", // catches szkola.int.pl, games.int.pl, etc. via suffix match
		"poczta.fm",
		"pisz.to",
		"pacz.to",
		"intmail.pl",
		"interiowy.pl",
		"adresik.net",
		"ogarnij.se",
		"vip.interia.pl",

		// ── Microsoft consumer accounts ───────────────────────────────────────────
		// Basic auth (plain password) permanently killed Sep 2024; OAuth2 only.
		// All plain-password LOGIN attempts return AUTH FAILED immediately.
		"outlook.com", "outlook.fr", "outlook.de", "outlook.it", "outlook.es",
		"outlook.co.uk", "outlook.co.nz", "outlook.co.th", "outlook.co.id",
		"outlook.com.au", "outlook.com.tr", "outlook.com.vn", "outlook.com.br",
		"outlook.at", "outlook.be", "outlook.cl", "outlook.cz", "outlook.dk",
		"outlook.hu", "outlook.ie", "outlook.in", "outlook.jp", "outlook.kr",
		"outlook.lv", "outlook.my", "outlook.ph", "outlook.pt", "outlook.ro",
		"outlook.sa", "outlook.sg", "outlook.sk",
		"hotmail.com", "hotmail.fr", "hotmail.de", "hotmail.it", "hotmail.es",
		"hotmail.co.uk", "hotmail.co.jp", "hotmail.co.nz", "hotmail.co.id",
		"hotmail.com.au", "hotmail.com.br", "hotmail.com.ar", "hotmail.com.tr",
		"hotmail.nl", "hotmail.be", "hotmail.se", "hotmail.no", "hotmail.dk",
		"hotmail.fi", "hotmail.gr", "hotmail.hu", "hotmail.lt", "hotmail.lv",
		"hotmail.pt", "hotmail.ro", "hotmail.sk", "hotmail.rs", "hotmail.hr",
		"live.com", "live.fr", "live.de", "live.it", "live.es",
		"live.co.uk", "live.co.jp", "live.co.za", "live.co.nz",
		"live.com.au", "live.com.ar", "live.com.mx", "live.com.pt", "live.com.br",
		"live.nl", "live.be", "live.at", "live.se", "live.dk", "live.no",
		"live.fi", "live.ca", "live.cl", "live.ph", "live.sg", "live.my",
		"live.in", "live.ie", "live.cn",
		"msn.com",
		"windowslive.com",
		"passport.com",

		// ── Yahoo / AOL ───────────────────────────────────────────────────────────
		// Basic auth killed May 2024 (Yahoo) / same era (AOL, owned by Yahoo).
		"yahoo.com", "ymail.com", "rocketmail.com",
		"yahoo.co.uk", "yahoo.co.in", "yahoo.co.jp", "yahoo.co.nz", "yahoo.co.id",
		"yahoo.com.au", "yahoo.com.br", "yahoo.com.ar", "yahoo.com.mx",
		"yahoo.com.sg", "yahoo.com.ph", "yahoo.com.hk", "yahoo.com.tw",
		"yahoo.com.vn", "yahoo.com.pe", "yahoo.com.co",
		"yahoo.fr", "yahoo.de", "yahoo.es", "yahoo.it", "yahoo.at",
		"yahoo.be", "yahoo.ca", "yahoo.gr", "yahoo.hu", "yahoo.ie",
		"yahoo.nl", "yahoo.no", "yahoo.ro", "yahoo.se", "yahoo.dk",
		"yahoo.fi", "yahoo.pt", "yahoo.lt", "yahoo.lv", "yahoo.ee",
		"aol.com", "aol.co.uk", "aol.de", "aol.fr", "aol.it", "aol.nl",

		// ── ProtonMail ────────────────────────────────────────────────────────────
		// No IMAP protocol at all (E2E encryption; IMAP would require server decryption).
		// Connection to IMAP port is refused or returns protocol error.
		"proton.me", "protonmail.com", "protonmail.ch", "pm.me", "proton.ch",

		// ── Tutanota / Tuta ───────────────────────────────────────────────────────
		// No IMAP — proprietary protocol only, by design.
		"tuta.com", "tutanota.com", "tutanota.de", "tutamail.com", "keemail.me",

		// ── QQ Mail (China) ───────────────────────────────────────────────────────
		// Requires a 16-char "authorization code" (not the QQ account password).
		// Plain password always returns AUTH FAILED.
		"qq.com",

		// ── NetEase Mail (China) ──────────────────────────────────────────────────
		// Requires authorization code + IMAP ID extension. Plain password rejected.
		// Even if LOGIN appears OK, SELECT returns "NO Unsafe Login" without the ID.
		"163.com", "126.com", "yeah.net", "188.com",
		"vip.163.com", "vip.126.com",

		// ── Ukr.net (Ukraine) ─────────────────────────────────────────────────────
		// App password required since ~2023; plain account password rejected at LOGIN.
		"ukr.net",
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
