package db

import (
	"database/sql"
	"strings"

	"github.com/cespare/xxhash/v2"
	_ "github.com/mattn/go-sqlite3"
)

// ServerInfo holds the IMAP server configuration for a domain.
type ServerInfo struct {
	Host     string
	Port     int
	Fallback bool // true = not in DB; Host is "imap.<domain>", Port is 993
}

// domainKey computes the xxHash64 lookup key matching the Servers.db schema:
// strip whitespace, lowercase, remove trailing dot, then int64(xxhash.Sum64String).
func domainKey(domain string) int64 {
	key := strings.TrimRight(strings.ToLower(strings.TrimSpace(domain)), ".")
	return int64(xxhash.Sum64String(key))
}

// BatchLookup opens dbPath read-only, queries the IMAP table for every domain,
// and returns a map[domain]ServerInfo. Domains absent from the DB get a fallback
// entry (Host="imap.<domain>", Port=993, Fallback=true).
// The DB connection is opened and closed within this call — caller holds no handle.
func BatchLookup(dbPath string, domains []string) (map[string]ServerInfo, error) {
	dsn := "file:" + dbPath + "?mode=ro&_journal_mode=WAL&cache=shared"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	stmt, err := db.Prepare("SELECT Server, Port FROM IMAP WHERE Domain = ?")
	if err != nil {
		return nil, err
	}
	defer stmt.Close()

	result := make(map[string]ServerInfo, len(domains))
	for _, domain := range domains {
		if _, seen := result[domain]; seen {
			continue
		}
		var server string
		var port int
		err := stmt.QueryRow(domainKey(domain)).Scan(&server, &port)
		if err == sql.ErrNoRows {
			result[domain] = ServerInfo{Host: "imap." + domain, Port: 993, Fallback: true}
			continue
		}
		if err != nil {
			return nil, err
		}
		result[domain] = ServerInfo{Host: server, Port: port}
	}
	return result, nil
}
