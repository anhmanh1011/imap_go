# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build, test, run

```bash
# CGo is required (mattn/go-sqlite3). Verify before building on a new env.
go env CGO_ENABLED   # must be 1

go build -o imap_checker .
go test ./... -race -count=1
go test ./internal/checker/ -run TestParseLine -v   # single test
go vet ./...

# End-to-end smoke (will hit real IMAP servers; expect Invalid for fake creds):
printf "test@gmail.com:wrongpass\n" > /tmp/c.txt
./imap_checker -input /tmp/c.txt -workers 2 -db ./Servers.db -out /tmp/out
```

`Servers.db` (~733 MiB) lives in the repo root and is gitignored. `BatchLookup` opens it read-only — never modify it. `Servers.db.md` documents its schema.

## Architecture: two-phase pipeline

The whole binary is built around one invariant: **the SQLite DB is opened, fully drained, and closed during Phase 1; Phase 2 never touches disk for lookups.**

- **Phase 1 (sequential, in `main.go`)** parses the credential file, dedups domains via `checker.UniqueDomains`, calls `db.BatchLookup` to build a `map[string]db.ServerInfo` for every unique domain, loads the optional proxy file, creates the 4 output files. The DB handle is opened *and closed inside* `BatchLookup` — `main.go` never holds a `*sql.DB`.
- **Phase 2 (`*workersFlag` goroutines)** drains a buffered `chan checker.Credential`. Each worker calls `checker.Checker.Check`, which does: domain map lookup → proxy pool pick → TCP dial (direct or HTTP CONNECT) → TLS handshake (implicit on 993, STARTTLS on 143) → IMAP `LOGIN` → classify → write result + increment progress.
- **Shutdown order** (`stopFlush() → stopBar() → writer.Close()`) matters. The `Start*` funcs returned by `progress.Bar.Start` and `result.Writer.StartAutoFlush` block until their goroutine has fully exited; do not "fix" them to return early — that re-introduces a write-after-close race against `Writer.Close`.

## Hot-path invariants (don't break)

- **`domainMap` is read-only after Phase 1.** Hundreds of workers read it without a lock. Any code that mutates it during Phase 2 is a race.
- **`result.Status` is the iota index into `Writer.bufs`, `Writer.mu`, `Writer.files`, and `fileNames`.** `Valid=0, Invalid=1, Error=2, HostNotFound=3`. Adding a status requires touching all four arrays in `internal/result/writer.go` and the `switch` in `Checker.Check`.
- **`db.domainKey` normalization must match the DB exactly:** `TrimSpace → ToLower → TrimRight "."` then `int64(xxhash.Sum64String(key))`. The `Servers.db` `Domain` column was built with this exact pipeline; any deviation produces zero matches.
- **`tlsCfg` is a package-level singleton** in `internal/checker/checker.go`. `InsecureSkipVerify: true` is intentional — the DB maps to ~14M domains with mixed cert configs. Don't allocate per call.
- **Retry rule in `tryLogin`:** network errors (`isNetErr=true`) retry once; IMAP `*imap.Error` (NO/BAD) does not. If the domain was a `Fallback` (not in DB, synthesized as `imap.<domain>:993`) and both attempts fail with network errors, `Check` promotes the status to `HostNotFound` instead of `Error`.

## go-imap/v2 quirks (v2.0.0-beta.8)

The library API differs from the docs in obvious places:

- `imapclient.New(conn, opts) *Client` returns one value, **not** `(*Client, error)`.
- STARTTLS is not `Client.StartTLS(cfg).Wait()` — use `imapclient.NewStartTLS(rawConn, &imapclient.Options{TLSConfig: tlsCfg})` which reads the greeting and upgrades in one call.
- The IMAP error type is `*imap.Error`, not `*imap.ResponseError`. Check with `errors.As(err, &imapErr)`.

If you upgrade `go-imap/v2`, re-verify these against the new source; the plan in `docs/superpowers/plans/` still cites the (incorrect) old API.

## Output format

Four files in `-out`:
- `valid.txt`, `invalid.txt`, `host_not_found.txt`: `user:pass\n`
- `error.txt`: `user:pass:reason\n` where `reason` is layer-prefixed (`dial:`, `tls:`, `starttls:`, `login:`) and sanitized (CR/LF stripped, capped at 200 B). Downstream parsers can split on the **first** two `:` only — passwords may legitimately contain `:`.

`Writer.Close` returns the first I/O error observed during the run via `errors.Join`. `main.go` logs it as a warning; treat a non-nil return as "results may be incomplete," not a soft warning.

## Design docs

The original spec and the task-by-task implementation plan live at:
- `docs/superpowers/specs/2026-06-02-imap-checker-design.md`
- `docs/superpowers/plans/2026-06-02-imap-checker.md`

The plan's code samples for `internal/checker/` are out of date vs the go-imap quirks above. Prefer the code over the plan when they disagree.
