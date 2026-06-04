# Telegram Bot Integration — Design Spec

**Date:** 2026-06-04  
**Status:** Approved

---

## 1. Overview

Standalone Go service (`cmd/tgbot/`) that acts as a Telegram userbot (MTProto via `gotd/td`). It monitors an input channel for `.txt` credential files, runs the IMAP checker pipeline for each file, and uploads `valid.txt` with a summary caption to a separate output channel.

Two operating modes run sequentially at startup then overlap:

1. **Backfill** — scans full input channel history, processes all unprocessed `.txt` files.
2. **Realtime** — subscribes to live updates, processes new `.txt` files as they arrive.

Both modes share a single sequential job queue so the checker is never run concurrently (each run already consumes thousands of workers and proxies).

---

## 2. Project Layout

```
cmd/tgbot/
└── main.go                  # entry point: load config, start bot

internal/
├── tgclient/
│   └── client.go            # gotd/td session load, connect, update dispatch, GetHistory
└── tgbot/
    ├── bot.go               # orchestrator: backfill loop + realtime update handler
    ├── processor.go         # runs checker pipeline for one input file
    └── state.go             # tgbot_state.db CRUD (processed message tracking)
```

Existing internal packages (`internal/checker`, `internal/db`, `internal/proxy`, `internal/result`, `internal/progress`) are reused directly by `processor.go` — no duplication.

---

## 3. Configuration

File `tgbot.env` (or environment variables):

```env
# Telegram (from my.telegram.org)
TG_API_ID=12345678
TG_API_HASH=abc123...
TG_SESSION_FILE=./tgbot_session.json

# Channels — username (@foo) or numeric ID (-1001234567890)
TG_INPUT_CHANNEL=@my_input_channel
TG_OUTPUT_CHANNEL=@my_output_channel

# Checker
WORKERS=2000
PROXIES_FILE=./proxies/rotating.txt
# PROXY_URL=https://...        # mutually exclusive with PROXIES_FILE
PROXY_REFRESH=10m
DB_PATH=./Servers.db
WORK_DIR=./tgbot_workdir       # temp dir, one subdir per run (<msg_id>/)

# State
STATE_DB=./tgbot_state.db
```

`PROXIES_FILE` and `PROXY_URL` are mutually exclusive (same constraint as the main binary). Bot exits with a fatal error if both are set.

---

## 4. State Database

File: `tgbot_state.db` (SQLite, created on first run).

```sql
CREATE TABLE IF NOT EXISTS processed (
    message_id   INTEGER PRIMARY KEY,
    channel_id   INTEGER NOT NULL,
    processed_at INTEGER NOT NULL,  -- unix timestamp (seconds)
    total_count  INTEGER NOT NULL DEFAULT 0,
    valid_count  INTEGER NOT NULL DEFAULT -1
);
```

`valid_count` sentinel values:
- `-1` — row inserted at job start, run did not complete (crash mid-run). Re-enqueued on next startup.
- `-2` — run completed but ended in a non-retryable error (parse error, DB error). Not re-enqueued; output channel notified.
- `≥ 0` — run completed successfully.

---

## 5. Telegram Client (`internal/tgclient`)

**Session:** Load from `TG_SESSION_FILE` at startup. No interactive auth. If the file is missing or the session is expired, the bot logs a fatal error and exits.

**Backfill:** Uses `messages.GetHistory` with `limit=100` per page, iterating from newest to oldest until an empty page is returned (end of history). For each message: skip if `message_id` is already in `state.db` (any `valid_count` value); enqueue if it is a `.txt` document not yet in `state.db`. This full-scan approach ensures files missed by a prior failed download are always retried.

**Realtime:** Handles `UpdateNewChannelMessage` from the input channel. Filters for `DocumentMedia` where the filename ends in `.txt`. Enqueues into `jobChan`. All other update types are ignored.

**Concurrency model:**

```
tgclient goroutine ──enqueue──▶ jobChan (buffered, cap=10)
                                        │
                                 processor goroutine (1x, sequential)
```

Backfill and realtime share the same `jobChan`; the processor does not distinguish the source.

---

## 6. Processor Pipeline (`internal/tgbot/processor.go`)

```
Download file → WORK_DIR/<msg_id>/input.txt
      │
      ▼
Insert state.db row (valid_count=-1)  ← crash-safe marker
      │
      ▼
checker.ParseFile + checker.FilterBlocked
      │
      ▼
db.BatchLookup  (opens + closes Servers.db, same as main binary)
      │
      ▼
proxy.LoadFile / proxy.StartURLPoller
      │
      ▼
Phase 2: WORKERS goroutines → chk.Check() → result.Writer
         (progress bar disabled — no terminal)
      │
      ▼
Read valid.txt → upload to output channel
      │
      ▼
Update state.db: total_count, valid_count
      │
      ▼
Delete WORK_DIR/<msg_id>/
```

`Process` signature:

```go
func Process(ctx context.Context, cfg Config, msgID int64, inputPath string) (total, valid int, err error)
```

On error: bot sends a short error message to the output channel and updates `state.db` with `valid_count=-2` to distinguish crash from in-progress.

---

## 7. Output Channel Message

```
✅ Done: msg#12345 | total: 8420 | valid: 312 | workers: 2000
File: original_filename.txt
```

The `valid.txt` file is sent as a document attachment alongside this caption. If `valid_count == 0`, the bot sends the caption only (no empty file attachment).

---

## 8. Dependencies

| Module | Purpose |
|---|---|
| `github.com/gotd/td` | MTProto userbot (session load, GetHistory, updates, file download/upload) |
| `github.com/joho/godotenv` | Load `tgbot.env` config file |
| `github.com/mattn/go-sqlite3` | State DB (already in go.mod) |

Existing deps (`go-imap/v2`, `xxhash`, `go-sqlite3`) unchanged.

---

## 9. Error Handling

| Scenario | Behaviour |
|---|---|
| Session file missing / expired | Fatal log, exit 1 |
| Input channel not found | Fatal log, exit 1 |
| File download fails | Log error, skip message (no state.db entry) |
| Checker parse error | Log error, mark `valid_count=-2`, notify output channel |
| Checker DB error | Log error, mark `valid_count=-2`, notify output channel |
| Upload fails | Log error, retry once after 5s; give up and log on second fail |
| `jobChan` full (10 slots) | Block enqueue — backpressure is intentional |

---

## 10. Startup Sequence

```
1. Load config from tgbot.env / env vars
2. Open state.db, run CREATE TABLE IF NOT EXISTS
3. Re-enqueue any rows with valid_count = -1 (incomplete prior runs)
4. Connect to Telegram (load session file)
5. Resolve input/output channel IDs
6. Start processor goroutine (drains jobChan)
7. Run backfill (paginate GetHistory, enqueue unprocessed files)
8. Enter realtime update loop (blocks until SIGINT/SIGTERM)
9. On shutdown: drain jobChan, finish current job, exit
```
