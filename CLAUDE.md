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
- `valid.txt`: `user:pass:imap_host:imap:port\n`
- `invalid.txt`, `host_not_found.txt`: `user:pass\n`
- `error.txt`: `user:pass:reason\n` where `reason` is layer-prefixed (`dial:`, `tls:`, `starttls:`, `login:`) and sanitized (CR/LF stripped, capped at 200 B). Downstream parsers can split on the **first** two `:` only — passwords may legitimately contain `:`.

`Writer.Close` returns the first I/O error observed during the run via `errors.Join`. `main.go` logs it as a warning; treat a non-nil return as "results may be incomplete," not a soft warning.

## Design docs

The original spec and the task-by-task implementation plan live at:
- `docs/superpowers/specs/2026-06-02-imap-checker-design.md`
- `docs/superpowers/plans/2026-06-02-imap-checker.md`

The plan's code samples for `internal/checker/` are out of date vs the go-imap quirks above. Prefer the code over the plan when they disagree.

---

## tgbot — Telegram userbot

Standalone bot that watches a Telegram input channel for `.txt` credential files, runs the IMAP checker on each, and uploads `valid.txt` to an output channel.

### Build & run

```bash
go build -o tgbot ./cmd/tgbot

# Run directly:
./tgbot tgbot.env

# Run via PM2 (preferred — autorestart, logs to logs/pm2-tgbot.*.log):
pm2 start ecosystem.config.js --only tgbot
pm2 logs tgbot
pm2 restart tgbot
pm2 stop tgbot
```

### Config — `tgbot.env` (gitignored)

```env
TG_API_ID=<from my.telegram.org>
TG_API_HASH=<from my.telegram.org>
TG_SESSION_FILE=./main.session        # gotd/td native JSON session (gitignored)
TG_INPUT_CHANNEL=txt_output           # channel display title (not @username)
TG_OUTPUT_CHANNEL=valid               # channel display title (not @username)
WORKERS=5000
PROXIES_FILE=./proxies/rotating.txt
DB_PATH=./Servers.db
WORK_DIR=./tgbot_workdir
STATE_DB=./tgbot_state.db
# Optional: inbox search mode — if set, only accounts with ≥1 email FROM this
# address are uploaded as "valid". Accepts full address or domain suffix.
# SEARCH_FROM=donotreply@godaddy.com
```

Channel names are resolved by **display title** via `messages.GetDialogs` — works with private channels. Do NOT use `@username` form.

`main.session` must be in gotd/td native `FileStorage` format (`{"Version":1,"Data":{...}}`). Telethon/Pyrogram `.session` files are incompatible.

### Architecture

```
Telegram input channel (txt_output)
        │  realtime UpdateNewChannelMessage
        │  backfill MessagesGetHistory
        ▼
  tgclient.Client           ← internal/tgclient/client.go
  resolveChannelByTitle()   ← searches dialogs by display title
        │
        ▼ Job{MessageID, FileName, Download closure}
  jobChan (cap=10)
        │
        ▼ (sequential — one checker run at a time)
  tgbot.Bot.processJob()    ← internal/tgbot/bot.go
    download → insert state(-1) → Process() → upload valid.txt
        │
        ▼
  tgbot.Process()           ← internal/tgbot/processor.go
    ParseFile → FilterBlocked → BatchLookup → N workers → result.Writer
    (progress bar disabled; logs CPM every 30s)
        │
        ▼
  Telegram output channel (valid)
    ✅ Done: msg#N | total: X | valid: Y
    [valid.txt attached]
```

### State DB — `tgbot_state.db`

Tracks processed message IDs. `valid_count` sentinels:
- `-1` = crash mid-run → deleted by `DeleteIncomplete` on restart → retried
- `-2` = non-retryable error → kept, output channel notified
- `≥0` = success

### Logs (PM2)

```
logs/pm2-tgbot.out.log   # stdout (unused — all output goes to stderr)
logs/pm2-tgbot.err.log   # all log lines including progress
```

Progress line format (every 30s):
```
tgbot: progress 48979/264675 | CPM inst=50746 avg=48978 | valid=15
```

Download line format:
```
tgbot: downloaded msg#684 "filename.txt" → 15.00 MB in 1.9s (7.7 MB/s)
```

### Spec & plan
- `docs/superpowers/specs/2026-06-04-tgbot-design.md`
- `docs/superpowers/plans/2026-06-04-tgbot.md`
