# IMAP Checker — Design Spec

**Date:** 2026-06-02  
**Status:** Approved

---

## 1. Overview

High-performance Go CLI tool that reads a `user:pass` credential file, resolves each domain's IMAP server from `Servers.db`, then concurrently attempts IMAP LOGIN for each credential — with optional HTTP proxy support.

---

## 2. CLI Interface

```
imap_checker \
  -input    credentials.txt \   # required: u:p file
  -workers  500 \               # required: number of goroutines
  -proxies  proxies.txt \       # optional: ip:port file
  -db       ./Servers.db \      # default: ./Servers.db
  -out      ./output            # default: ./output
```

---

## 3. Architecture

### 3.1 Two-Phase Design

**Phase 1 — Setup (sequential, fast):**
1. Parse all credentials from input file into `[]Credential`
2. Extract unique domains
3. Batch lookup all unique domains against `Servers.db` → `map[string]ServerInfo`
4. Domains not found → `ServerInfo{Fallback: true, Host: "imap."+domain, Port: 993}`
5. Load proxy list from file (if provided)
6. Close SQLite connection — no DB I/O during Phase 2

**Phase 2 — Check (concurrent):**
- Worker pool of N goroutines drains a credential channel
- Each worker uses in-memory `domainMap` (zero lock contention)
- Results written to 4 output files via buffered writers

### 3.2 Goroutine Topology

```
main
 ├─ feeder (1x)     file → credChan[buffered]
 ├─ worker (Nx)     credChan → check → resultChan[buffered]
 ├─ writer (1x)     resultChan → 4 output files
 └─ progress (1x)   atomic counters → stderr, every 200ms
```

### 3.3 Shutdown Sequence

1. feeder exhausts file → closes `credChan`
2. workers detect closed channel → call `wg.Done()`
3. `wg.Wait()` → closes `resultChan`
4. writer drains `resultChan` → flushes all buffers
5. progress goroutine receives done signal → final render

---

## 4. Component Design

### 4.1 `internal/db` — Domain Lookup

- Open `Servers.db` as **read-only**, WAL mode
- For each unique domain, compute lookup key:
  ```go
  key := strings.TrimRight(strings.ToLower(strings.TrimSpace(domain)), ".")
  h   := int64(xxhash.Sum64String(key))   // uint64 wrap-around to int64
  ```
- `SELECT Server, Port FROM IMAP WHERE Domain = ?`
- Returns `map[string]ServerInfo`
- DB closed before Phase 2 begins

**Dependency:** `github.com/cespare/xxhash/v2`, `github.com/mattn/go-sqlite3`

### 4.2 `internal/proxy` — Proxy Pool

- Loads `[]string` of `ip:port` entries at startup
- `Next()` picks next proxy via `atomic.Uint64` round-robin — lock-free
- Empty pool → `Next()` returns `""` → direct dial

### 4.3 `internal/checker` — IMAP Worker

Per-credential flow:
```
lookup domainMap → ServerInfo
pick proxy (may be empty)
dial TCP → [HTTP CONNECT if proxy] → TLS handshake → IMAP LOGIN
  ├─ OK  → Valid
  ├─ NO/BAD → Invalid
  └─ network error → retry once (wait 1s) → Error on second fail
       └─ if ServerInfo.Fallback && both attempts fail → HostNotFound
```

**Port logic:**
- Port 993 → implicit TLS (`tls.Dial`)
- Port 143 → STARTTLS (`STARTTLS` command after plain connect)
- Other ports from DB → implicit TLS (safe default)

**Timeout:** 30 seconds (dial + TLS + IMAP greeting + LOGIN response)

**Dependency:** `github.com/emersion/go-imap/v2`

### 4.4 `internal/result` — Buffered Writers

- 4 `bufio.Writer` instances, one per output file
- Each writer protected by `sync.Mutex`
- Flush every 100ms via background ticker, and on shutdown
- Output format:
  - `valid.txt` → `user:pass:imap_host:imap:port`
  - `invalid.txt`, `host_not_found.txt` → `user:pass`
  - `error.txt` → `user:pass:reason`

### 4.5 `internal/progress` — Progress Bar

- 5 `atomic.Int64` counters: total, valid, invalid, error, hostNotFound
- Render goroutine ticks every 200ms, overwrites current line with `\r`
- Format:
  ```
  [=========>          ] 4500/10000 (45%) | valid: 123 | invalid: 4200 | error: 45 | hnf: 132 | 1250 acc/s
  ```
- Speed (`acc/s`) computed from total processed delta between ticks

---

## 5. Data Flow

```
credentials.txt          Servers.db
      │                      │
      ▼                      ▼
 Parse []Credential     Batch lookup
      │                 map[domain]ServerInfo
      └──────────┬───────────┘
                 ▼
           credChan (buffered)
                 │
         ┌───────┴────────┐
         │  Worker × N    │
         │  domainMap     │  ← in-memory, no lock
         │  proxyPool     │  ← atomic round-robin
         │  IMAP dial     │
         └───────┬────────┘
                 ▼
           resultChan (buffered)
                 │
           Result Writer
                 │
      ┌──────────┼──────────┬────────────┐
      ▼          ▼          ▼            ▼
 valid.txt  invalid.txt  error.txt  host_not_found.txt
```

---

## 6. Result Classification

| Status | Condition | Output File |
|---|---|---|
| `Valid` | IMAP LOGIN returns `OK` | `valid.txt` |
| `Invalid` | IMAP LOGIN returns `NO` or `BAD` | `invalid.txt` |
| `Error` | Network/TLS failure after 1 retry | `error.txt` |
| `HostNotFound` | Not in DB + fallback `imap.<domain>:993` also fails | `host_not_found.txt` |

---

## 7. Dependencies

| Module | Version | Purpose |
|---|---|---|
| `github.com/cespare/xxhash/v2` | latest | xxHash64 domain key |
| `github.com/mattn/go-sqlite3` | latest | SQLite3 CGo driver |
| `github.com/emersion/go-imap/v2` | latest | IMAP client |
| `golang.org/x/net` | latest | HTTP proxy dialer |

---

## 8. Project Layout

```
imap_checker/
├── main.go
├── go.mod
├── go.sum
├── internal/
│   ├── checker/
│   │   └── checker.go      # IMAP dial + login logic
│   ├── db/
│   │   └── db.go           # Servers.db batch lookup
│   ├── proxy/
│   │   └── pool.go         # Round-robin proxy pool
│   ├── result/
│   │   └── writer.go       # Buffered 4-file output
│   └── progress/
│       └── bar.go          # Progress bar renderer
├── Servers.db
├── Servers.db.md
└── docs/
    └── superpowers/
        └── specs/
            └── 2026-06-02-imap-checker-design.md
```

---

## 9. Performance Characteristics

- **SQLite lookups:** O(1) per domain (B-tree PK on hash), done once at startup
- **Worker contention:** zero on `domainMap` (read-only map after Phase 1)
- **Proxy contention:** lock-free via `atomic.Uint64`
- **Result writes:** per-file mutex, 4KB buffer, 100ms flush — write latency not on critical path
- **Memory:** `domainMap` holds only unique domains from input file (not all 14M rows)
- **Bottleneck:** network I/O (IMAP dial + login) — scales linearly with `-workers`
