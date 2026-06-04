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
TG_SESSION_FILE=./main.session   # gotd/td native FileStorage JSON

# Channels — @username form only (v1); numeric IDs (-100…) require a dialogs
# lookup for the access hash and are deferred to a future version.
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

**Session:** Load from `TG_SESSION_FILE` at startup via gotd's `session.FileStorage`. The file **must** be in gotd/td's native session format (the `{"Version":1,"Data":{...,"AuthKey":...,"AuthKeyID":...,"Salt":...}}` JSON layout, e.g. `main.session`). Telethon/Pyrogram `.session` files are **not** compatible and would have to be converted first. No interactive auth. If the file is missing or the session is expired/revoked, the bot logs a fatal error and exits.

**Backfill:** Uses `messages.GetHistory` with `limit=100` per page, iterating from newest to oldest until an empty page is returned (end of history). For each message: skip if `message_id` is already in `state.db` (any `valid_count` value); enqueue if it is a `.txt` document not yet in `state.db`. This full-scan approach ensures files missed by a prior failed download are always retried.

**Realtime:** Handles `UpdateNewChannelMessage` from the input channel. Filters for `DocumentMedia` where the filename ends in `.txt`. Enqueues into `jobChan`. All other update types are ignored.

**Realtime-before-backfill ordering (no gap):** The update handler is registered and active **before** backfill begins, so any file posted while backfill is still scanning is captured by realtime instead of being lost. A message could therefore be enqueued by both paths; the processor dedups against `state.db` (skip if `message_id` already present), making double-enqueue harmless.

**Concurrency model:**

```
tgclient goroutine (realtime) ──┐
                                 ├──enqueue──▶ jobChan (buffered, cap=10)
backfill goroutine ─────────────┘                    │
                                              processor goroutine (1x, sequential)
```

Backfill and realtime share the same `jobChan`; the processor does not distinguish the source and dedups by `message_id` against `state.db` before doing any work.

---

## 6. Processor Pipeline (`internal/tgbot/processor.go`)

`Process` runs **only the checker** and knows nothing about Telegram. It takes a local input file and returns the result counts plus the path to the produced `valid.txt`. The orchestrator (`bot.go`) owns all Telegram I/O (download, upload, state writes) and temp-dir lifecycle. This keeps `processor.go` free of any `tgclient` dependency and unit-testable with a plain file.

```go
type Result struct {
    Total        int
    Valid        int
    ValidTxtPath string // path to valid.txt under WORK_DIR/<msg_id>/output/
}

func Process(ctx context.Context, cfg Config, inputPath string) (Result, error)
```

`Process` internals: `checker.ParseFile` + `checker.FilterBlocked` → `db.BatchLookup` (opens + closes `Servers.db`, same as main binary) → use the shared `*proxy.Pool` from `cfg` (pre-built at startup, see §6.1) → Phase 2 with `WORKERS` goroutines → `chk.Check()` → `result.Writer`. The progress bar is disabled (no terminal); progress is logged to stdout instead.

**Orchestration in `bot.go`** (per job pulled from `jobChan`):

```
Download file → WORK_DIR/<msg_id>/input.txt
      │
      ▼
Insert state.db row (valid_count=-1)        ← crash-safe marker
      │
      ▼
res, err := Process(ctx, cfg, inputPath)    ← checker only, no Telegram
      │
      ├─ err != nil → update state.db valid_count=-2, notify output channel, cleanup, continue
      │
      ▼
Upload res.ValidTxtPath to output channel with caption   (skip file if res.Valid == 0)
      │
      ▼
Update state.db: total_count=res.Total, valid_count=res.Valid
      │
      ▼
Delete WORK_DIR/<msg_id>/
```

### 6.1 Proxy pool lifecycle

The proxy pool is built **once at bot startup**, not per job:

- `PROXIES_FILE` → `proxy.LoadFile` once; the resulting `*proxy.Pool` is shared (read-only after load) across every `Process` call.
- `PROXY_URL` → `proxy.StartURLPoller` is started **once** at startup with a single background refresh goroutine, and its stop func is deferred to bot shutdown. It is **not** started/stopped per job (which would leak goroutines and never reach the first refresh on short files).

`Process` receives the already-built `*proxy.Pool` via `cfg`; it never starts or stops a poller itself.

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
3. Connect to Telegram (load session file)
4. Resolve input/output channel IDs
5. Start processor goroutine (drains jobChan)
6. Re-enqueue any rows with valid_count = -1 (incomplete prior runs)
   — done AFTER the processor exists so a large backlog cannot block startup
7. Register realtime update handler (active before backfill — closes the gap)
8. Run backfill in its own goroutine (paginate GetHistory, enqueue unprocessed files)
9. Block on realtime update loop until SIGINT/SIGTERM
10. On shutdown: stop enqueueing, drain jobChan, finish current job, exit
```

Ordering rationale: the processor goroutine (step 5) must exist before anything enqueues (steps 6–8), otherwise a backlog larger than `jobChan`'s capacity (10) would deadlock startup. The realtime handler (step 7) is registered before backfill (step 8) so no live message is missed during the scan.
