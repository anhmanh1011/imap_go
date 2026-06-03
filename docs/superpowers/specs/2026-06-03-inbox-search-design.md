# Inbox Search — Design Spec

**Date:** 2026-06-03  
**Status:** Approved

## Overview

A standalone binary `inbox_search` that takes a list of verified IMAP credentials (`valid.txt`) and searches each account's INBOX for emails from a target domain (e.g., `*@godaddy.com`). Output is a list of accounts with match counts.

## Usage

```bash
./inbox_search \
  -input  valid.txt \
  -workers 50 \
  -target  godaddy.com \
  -out    ./search_out \
  -db     ./Servers.db \
  [-proxies proxies.txt]
```

## Flags

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `-input` | yes | — | Credential file (`user:pass` per line) |
| `-workers` | yes | — | Concurrent goroutines (hard cap 8000) |
| `-target` | yes | — | Domain to search, e.g. `godaddy.com` → searches `FROM "@godaddy.com"` |
| `-out` | no | `./search_out` | Output directory |
| `-db` | no | `./Servers.db` | Path to IMAP server DB |
| `-proxies` | no | — | HTTP-CONNECT or SOCKS5 proxy file |

## Architecture

Two-phase pipeline, matching `imap_checker` design:

### Phase 1 — Sequential setup

1. Parse credential file via `checker.ParseFile`
2. Deduplicate domains via `checker.UniqueDomains`
3. DB lookup via `db.BatchLookup` — builds `map[string]db.ServerInfo`, then closes DB
4. Load proxy pool
5. Create output writer (`internal/searcher.Writer`)
6. Start progress bar

### Phase 2 — Concurrent search (N workers)

Each worker pulls a `checker.Credential` from a buffered channel and calls `searcher.Searcher.Search`:

1. **Dial** — TCP (tcp4) to IMAP server, optionally through proxy
2. **TLS/STARTTLS** — port 993 implicit TLS, port 143 STARTTLS (same as checker)
3. **LOGIN** — `imapclient.Login(user, pass).Wait()`; on IMAP NO/BAD → `error`
4. **SELECT INBOX** — `client.Select("INBOX", nil).Wait()`; on failure → `error`
5. **SEARCH** — `client.Search(&imap.SearchCriteria{Header: [{Key:"From", Value:"@<target>"}]}, nil).Wait()`
6. Classify result:
   - N > 0 → `found` (write `user:pass:N` to `found.txt`)
   - N = 0 → `not_found` (write `user:pass` to `not_found.txt`)
   - error → write `user:pass:reason` to `error.txt`

## Output Files

All written to the `-out` directory:

| File | Format | Description |
|------|--------|-------------|
| `found.txt` | `user:pass:N` | Accounts with N emails from target domain |
| `not_found.txt` | `user:pass` | Accounts with no emails from target domain |
| `error.txt` | `user:pass:reason` | Login/search failures |

## New Package: `internal/searcher`

| File | Responsibility |
|------|---------------|
| `searcher.go` | `Searcher` struct + `Search(cred)` method — dial, TLS, LOGIN, SELECT, SEARCH |
| `writer.go` | `Writer` struct — 3 output files (found/not_found/error), auto-flush, thread-safe |

Reused from existing code: `internal/db`, `internal/proxy`, `internal/progress`, `internal/checker` (ParseFile, UniqueDomains, dial logic).

## Progress Bar

Reuses `internal/progress.Bar`. Counters: `found`, `not_found`, `error`.

## Error Handling

Same conventions as `imap_checker`:
- Network errors → retry once (with proxy rotation)
- IMAP NO/BAD → no retry, classify as `error`
- Reason strings: layer-prefixed (`dial:`, `tls:`, `starttls:`, `login:`, `search:`), CR/LF stripped, capped at 200 B

## Timeouts

Reuse `dialTimeout = 10s` from checker. No new timeout constants needed.
