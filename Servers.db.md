# `Servers.db` — Tài liệu kỹ thuật

> Lookup table **domain → mail server config** cho 3 giao thức IMAP / POP / SMTP.
> File SQLite, khoá tra cứu là **xxHash64 (seed = 0)** của domain đã lowercase.

---

## 1. Thông tin file

| Thuộc tính        | Giá trị                                  |
| ----------------- | ---------------------------------------- |
| Định dạng         | SQLite 3                                 |
| Đường dẫn         | `/root/imap_checker/Servers.db`          |
| Kích thước        | 768 888 832 B ≈ **733.3 MiB**            |
| Page size         | 4096 B                                   |
| Page count        | 187 717                                  |
| Freelist          | 0 (không có page rác)                    |
| Encoding          | UTF-8                                    |
| Journal mode      | WAL (đã checkpoint sạch, không có `-wal/-shm`) |
| SQLite engine     | ≥ 3.46                                   |

---

## 2. Schema

### 2.1. Bảng `IMAP`
```sql
CREATE TABLE "IMAP" (
    "Domain" INTEGER NOT NULL UNIQUE,   -- xxHash64(lowercase(domain), seed=0) cast sang INT64
    "Server" TEXT    NOT NULL,          -- hostname IMAP, ví dụ 'imap.gmail.com'
    "Port"   INTEGER NOT NULL DEFAULT 993,
    PRIMARY KEY("Domain")
) WITHOUT ROWID;
CREATE INDEX idx_imap ON IMAP(Domain);   -- ⚠ index thừa, trùng PK
```

### 2.2. Bảng `POP`
```sql
CREATE TABLE "POP" (
    "Domain" INTEGER NOT NULL UNIQUE,
    "Server" TEXT    NOT NULL,
    "Port"   INTEGER NOT NULL DEFAULT 995,
    PRIMARY KEY("Domain")
) WITHOUT ROWID;
CREATE INDEX idx_pop ON POP(Domain);     -- ⚠ index thừa
```

### 2.3. Bảng `SMTP`
```sql
CREATE TABLE "SMTP" (
    "Domain" INTEGER NOT NULL UNIQUE,
    "Server" TEXT    NOT NULL,
    "Port"   INTEGER NOT NULL DEFAULT 587,
    PRIMARY KEY("Domain")
) WITHOUT ROWID;
CREATE INDEX idx_smtp ON SMTP(Domain);   -- ⚠ index thừa
```

### 2.4. Bảng thống kê SQLite
- `sqlite_stat1`, `sqlite_stat4` — sinh tự động bởi `ANALYZE`, không cần can thiệp.

### 2.5. Đặc điểm chung
- `WITHOUT ROWID` + `PRIMARY KEY(Domain)` ⇒ bảng được lưu như **B-tree clustered** trực tiếp trên `Domain`, không nhân đôi rowid → tiết kiệm dung lượng & tăng tốc tra cứu.
- Vì cùng giản đồ, 3 bảng có thể được abstract qua 1 hàm `lookup(proto, domain)` duy nhất.

---

## 3. Khoá `Domain` — xxHash64 seed 0

### 3.1. Đặc điểm
- Kiểu lưu: **`INTEGER` 8-byte signed (BIGINT / INT64)**, không phải “BIGINT âm”.
- Phân phối: ~50 % âm / ~50 % dương — đặc trưng của hash 64-bit đều chia ngẫu nhiên rồi ép vào miền signed.
- Top-4-bit histogram đều ≈ 6.25 % cho cả 16 bucket ⇒ hash chất lượng cao.
- Span: phủ ~99.99 % toàn dải `[-2⁶³, 2⁶³−1]`.
- Không có giá trị 0 trong dataset hiện tại.

### 3.2. Thuật toán đã xác minh
**`xxh64(lowercase(domain), seed=0)`**

Đầu vào yêu cầu chuẩn hoá:
1. `domain.strip()` — bỏ khoảng trắng đầu/cuối
2. `domain.lower()` — RFC tên miền case-insensitive
3. (Khuyến nghị) `idna.encode()` nếu là IDN/Unicode
4. Bỏ dấu chấm trailing (`example.com.` → `example.com`)

Sau đó ép `uint64` xuống `int64`:
```python
u = xxhash.xxh64(domain_normalized.encode(), seed=0).intdigest()
signed = u - (1 << 64) if u >= (1 << 63) else u
```

### 3.3. Test vector (đã xác nhận khớp DB)
| Domain          | xxh64 seed=0 (signed)     | Trả về                     |
| --------------- | ------------------------: | -------------------------- |
| `gmail.com`     |   2 691 187 859 986 816 277 | `imap.gmail.com:993`       |
| `outlook.com`   |  -4 558 591 710 954 502 866 | `outlook.office365.com:993`|
| `hotmail.com`   |  -6 687 126 143 800 646 354 | `outlook.office365.com:993`|
| `live.com`      |  -5 241 312 411 078 458 062 | `outlook.office365.com:993`|
| `yahoo.com`     |   8 509 464 350 704 277 843 | `imap.mail.yahoo.com:993`  |
| `yandex.ru`     |   7 406 932 364 908 842 369 | `imap.yandex.ru:993`       |
| `yandex.com`    |  -7 664 060 068 365 009 508 | `imap.yandex.ru:993`       |
| `1und1.de`      |  -7 990 607 681 827 451 256 | `imap.1und1.de:993`        |
| `1and1.com`     |  -7 572 530 844 826 433 009 | `imap.1und1.de:993`        |
| `strato.de`     |   3 408 485 381 746 396 416 | `imap.strato.de:993`       |
| `zoho.com`      |   3 739 670 296 600 904 734 | `imap.zoho.com:993`        |
| `mail.com`      |   2 971 484 240 372 079 340 | `imap.mail.com:993`        |
| `icloud.com`    |  -2 577 585 013 351 063 962 | `imap.mail.icloud.com:993` |

---

## 4. Quy mô dữ liệu

| Bảng | Rows         | Unique Servers | Min Domain                  | Max Domain                 |
| ---- | -----------: | -------------: | --------------------------: | -------------------------: |
| IMAP | **14 001 512** |    8 237 342 | -9 223 370 809 027 383 137  | 9 223 372 015 544 770 571  |
| POP  |      611 445 |      484 923  | -9 223 284 878 376 503 306  | 9 223 359 312 821 797 907  |
| SMTP |        6 784 |        5 940  | -9 221 020 953 867 555 146  | 9 215 785 877 270 822 002  |

### Cross-protocol overlap
| Tập hợp                    | Số domain |
| -------------------------- | --------: |
| IMAP ∩ POP                 |   103 777 |
| IMAP ∩ SMTP                |     5 805 |
| IMAP ∩ POP ∩ SMTP          |     2 926 |

> **Bảng SMTP gần rỗng** — tỉ lệ IMAP : SMTP ≈ 2 064 : 1; pipeline ingest SMTP gần như đã hỏng.

---

## 5. Phân phối Port

### IMAP
| Port  | Rows       | Ghi chú             |
| ----: | ---------: | ------------------- |
| 993   | 8 278 133  | IMAPS (TLS implicit)|
| 143   | 5 723 353  | IMAP STARTTLS / plain|
| 995   |       20   | ⚠ POP3S lẫn vào IMAP |
| 578   |        2   | ⚠ Typo của 587?     |
| 110   |        2   | ⚠ POP3 lẫn vào IMAP |
| 1143  |        1   | ⚠ `127.0.0.1` dev   |
| 587   |        1   | ⚠ SMTP lẫn vào IMAP |

### POP
| Port  | Rows    | Ghi chú             |
| ----: | ------: | ------------------- |
| 110   | 375 616 | POP3 cleartext      |
| 995   | 235 796 | POP3S               |
| 993   |     24  | ⚠ IMAPS lẫn         |
| 485   |      4  | ⚠ Không chuẩn (nhầm 465?) |
| 143   |      4  | ⚠ IMAP lẫn          |
| 500   |      1  | ⚠ Rác (`ya.ebal.imap`) |

### SMTP
| Port  | Rows  | Ghi chú                    |
| ----: | ----: | -------------------------- |
| 587   | 4 922 | Submission (STARTTLS)      |
| 465   | 1 102 | Submissions (TLS implicit) |
| 25    |   706 | ⚠ MTA, không phải client   |
| 2525  |     2 | Submission thay thế        |
| 1025  |     1 | Bất thường                 |
| *text* |   51 | **⚠ Lỗi shift cột (xem §6.1)** |

---

## 6. Vấn đề chất lượng dữ liệu

### 6.1. Lỗi shift cột SMTP (51 dòng)
SQLite có *type affinity* chứ không strict typing → text lọt vào cột `Port INTEGER`:
```
domain=-4531704443227840870  server='yeah.net'        port='yeah.net'
domain=-5851079123573340106  server='aol.de'          port='aol.de'
domain=-6531262198220852085  server='toucansurf.com'  port='toucansurf.com'
…
```
**Hành động:** xoá 51 dòng, import lại đúng cột, thêm `CHECK(typeof(Port)='integer' AND Port BETWEEN 1 AND 65535)`.

### 6.2. Server “rác”
| Server                  | Rows | Cách xử lý                       |
| ----------------------- | ---: | -------------------------------- |
| `'admin'`               |  44  | Xoá                              |
| `'mail'`                |   8  | Xoá hoặc resolve                 |
| `''` (rỗng)             |   2  | Xoá                              |
| `'localhost'`           |   1  | Xoá                              |
| `'${imap_server}'`      |   1  | Xoá (placeholder template chưa render) |
| `'imap.outlook.com '`   |   1  | Trim                             |
| `'imap.videotron.ca '`  |   1  | Trim                             |
| `'mail.teamvincent.com '` | 1  | Trim                             |

### 6.3. Cross-protocol contamination
- `pop.bizmail.nifcloud.com:995` xuất hiện **16 lần trong bảng IMAP** — phải nằm trong POP.
- `imap.gmail.com:993`, `outlook.office365.com:993`, `imap.163.com:993` xuất hiện trong **bảng POP**.
- `smtp.zebra.lt:587` xuất hiện trong IMAP.
- `127.0.0.1:1143` — leak localhost.

### 6.4. Port không hợp lệ
- Port 578 (2 dòng, `imap.mail.today.com`, `imap.mail.tuesday.com`) — nghi typo 587.
- Port 485 (`pop.proximus.be` ×4) — nghi nhầm 465.
- Port 500 (`ya.ebal.imap`) — entry troll.

---

## 7. Cách dùng — code mẫu

### 7.1. Python
```python
import sqlite3, xxhash

DB = "/root/imap_checker/Servers.db"

def lookup(domain: str, proto: str = "IMAP"):
    """proto ∈ {'IMAP','POP','SMTP'}"""
    key = domain.strip().lower().rstrip(".")
    u   = xxhash.xxh64(key.encode(), seed=0).intdigest()
    s   = u - (1 << 64) if u >= (1 << 63) else u          # uint64 → int64
    with sqlite3.connect(DB) as c:
        row = c.execute(
            f"SELECT Server, Port FROM {proto} WHERE Domain = ?", (s,)
        ).fetchone()
    return row    # (Server, Port) hoặc None

print(lookup("gmail.com", "IMAP"))    # ('imap.gmail.com', 993)
print(lookup("outlook.com", "IMAP"))  # ('outlook.office365.com', 993)
```

### 7.2. Go
```go
import (
    "database/sql"
    "strings"
    "github.com/cespare/xxhash/v2"
    _ "github.com/mattn/go-sqlite3"
)

func Lookup(db *sql.DB, proto, domain string) (server string, port int, err error) {
    key := strings.TrimRight(strings.ToLower(strings.TrimSpace(domain)), ".")
    h   := int64(xxhash.Sum64String(key))                  // wrap-around tự nhiên
    err  = db.QueryRow(
        "SELECT Server, Port FROM "+proto+" WHERE Domain = ?", h,
    ).Scan(&server, &port)
    return
}
```

### 7.3. C
```c
#include <sqlite3.h>
#include "xxhash.h"

uint64_t u = XXH64(domain, strlen(domain), /*seed=*/0);
int64_t  k = (int64_t)u;
sqlite3_stmt *stmt;
sqlite3_prepare_v2(db, "SELECT Server, Port FROM IMAP WHERE Domain=?", -1, &stmt, 0);
sqlite3_bind_int64(stmt, 1, k);
if (sqlite3_step(stmt) == SQLITE_ROW) {
    const char *server = (const char *)sqlite3_column_text(stmt, 0);
    int port           = sqlite3_column_int(stmt, 1);
}
sqlite3_finalize(stmt);
```

---

## 8. Khuyến nghị tối ưu

| Việc cần làm                                                       | Lợi ích                             |
| ------------------------------------------------------------------ | ----------------------------------- |
| `DROP INDEX idx_imap; DROP INDEX idx_pop; DROP INDEX idx_smtp;`    | Bỏ index trùng PK, tiết kiệm vài chục MiB, tăng tốc insert |
| Thêm `CHECK(typeof(Port)='integer' AND Port BETWEEN 1 AND 65535)`  | Chặn shift cột                      |
| Thêm `CHECK(Server GLOB '*.*' AND Server NOT GLOB '* *')`          | Chặn server “rác” (`'mail'`, `'admin'`, có khoảng trắng) |
| Re-classify cross-protocol contamination dựa trên Port chuẩn       | Đặt đúng bảng                       |
| Re-import SMTP từ nguồn                                            | Khắc phục mất cân đối 14 M : 6 K     |
| `PRAGMA integrity_check; PRAGMA wal_checkpoint(TRUNCATE);` định kỳ | Bảo trì                              |
| `VACUUM;` sau khi xoá rác lớn                                       | Trả lại dung lượng                   |
| `ANALYZE;`                                                         | Cập nhật `sqlite_stat1/4`            |

---

## 9. Top server (cảm quan nguồn dữ liệu)

### IMAP (top 10)
| Server                  | Rows      |
| ----------------------- | --------: |
| imap.gmail.com          | 1 809 369 |
| outlook.office365.com   | 1 710 291 |
| imap.1und1.de           |   344 389 |
| imap.strato.de          |   248 589 |
| imap.secureserver.net   |   224 218 |
| imap.mail.yahoo.com     |   100 366 |
| imap.1and1.com          |    97 608 |
| imap.yandex.com         |    91 127 |
| secure.emailsrvr.com    |    88 196 |
| imap.one.com            |    83 622 |

### POP (top 10)
| Server                  | Rows   |
| ----------------------- | -----: |
| eu-pop.mimecast.com     | 65 674 |
| pop3.lolipop.jp         | 39 264 |
| za-pop.mimecast.co.za   |  4 926 |
| pop3.muumuu-mail.com    |  1 730 |
| pop3.poczta.onet.pl     |  1 707 |
| mailapp.hiworks.co.kr   |    654 |
| pop.titan.email         |    566 |
| pop.bizmail.nifcloud.com|    425 |
| pop3s.aruba.it          |    395 |
| pop.goope.jp            |    222 |

### SMTP (top 10)
| Server                  | Rows |
| ----------------------- | ---: |
| smtp.mail.com           | 191  |
| smtp.office365.com      |  93  |
| smtp.jubii.dk           |  86  |
| mail.telenor.dk         |  79  |
| posteo.de               |  50  |
| smtp-auth.tiki.ne.jp    |  32  |
| smtp.mail.yahoo.com     |  26  |
| smtp.gmx.com            |  21  |
| smtp.aol.com            |  18  |
| smtp.poczta.onet.pl     |  16  |

---

## 10. Lưu ý vận hành / pháp lý

- DB là **one-way lookup** (không thể đảo từ hash về domain mà không brute-force qua wordlist).
- Corpus 14 M domain rất phù hợp cho **mail client autoconfig / anti-phish / mail server inventory**.
- Nếu mục đích là **credential checking**, đảm bảo có ủy quyền hợp pháp; không dùng cho credential stuffing.
- Trước khi public-share file: xem lại các entry chứa `127.0.0.1`, hostname nội bộ, hostname trên `.local`/`.lan`, để không lộ topology nội bộ.

---

*Tài liệu được sinh từ phân tích trực tiếp file `Servers.db`.*
