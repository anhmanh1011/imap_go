package checker

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"imap_checker/internal/db"
	"imap_checker/internal/progress"
	"imap_checker/internal/proxy"
	"imap_checker/internal/result"
)

const (
	dialTimeout  = 30 * time.Second
	retryWait    = time.Second
	maxAttempts  = 2
	connectMaxHdr = 4096
)

// tlsCfg is shared across all IMAP connections — stateless, safe to reuse.
// Hoisted out of doLogin to avoid one allocation per credential.
var tlsCfg = &tls.Config{InsecureSkipVerify: true} //nolint:gosec

// Credential is a parsed login entry from the input file.
type Credential struct {
	User   string
	Pass   string
	Domain string
}

// parseLine parses a single "user:pass" line.
// Splits on the first ":" so passwords may contain ":".
// Returns ok=false for lines without "@" in user or without ":".
func parseLine(line string) (user, pass, domain string, ok bool) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return
	}
	user = line[:idx]
	pass = line[idx+1:]
	at := strings.LastIndex(user, "@")
	if at < 0 {
		user, pass = "", ""
		return
	}
	domain = user[at+1:]
	ok = true
	return
}

// ParseFile reads a credential file (one "user:pass" per line) and returns
// all valid entries. Malformed lines are silently skipped.
func ParseFile(path string) ([]Credential, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var creds []Credential
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		user, pass, domain, ok := parseLine(strings.TrimSpace(sc.Text()))
		if ok {
			creds = append(creds, Credential{User: user, Pass: pass, Domain: domain})
		}
	}
	return creds, sc.Err()
}

// UniqueDomains returns the deduplicated list of domains from creds,
// preserving first-occurrence order.
func UniqueDomains(creds []Credential) []string {
	seen := make(map[string]struct{}, len(creds))
	var out []string
	for _, c := range creds {
		if _, ok := seen[c.Domain]; !ok {
			seen[c.Domain] = struct{}{}
			out = append(out, c.Domain)
		}
	}
	return out
}

// Checker performs IMAP login attempts using an in-memory domain map.
type Checker struct {
	domainMap map[string]db.ServerInfo
	proxyPool *proxy.Pool
	results   *result.Writer
	bar       *progress.Bar
}

// New creates a Checker. domainMap must be populated before Phase 2 begins.
func New(domainMap map[string]db.ServerInfo, pool *proxy.Pool, writer *result.Writer, bar *progress.Bar) *Checker {
	return &Checker{domainMap: domainMap, proxyPool: pool, results: writer, bar: bar}
}

// Check performs an IMAP login for cred, writes the result, and updates the progress bar.
func (c *Checker) Check(cred Credential) {
	info := c.domainMap[cred.Domain]

	res := result.Result{User: cred.User, Pass: cred.Pass}
	status, reason := c.tryLogin(cred, info)

	// If domain was a fallback and both attempts failed, classify as HostNotFound.
	if status == result.Error && info.Fallback {
		res.Status = result.HostNotFound
	} else {
		res.Status = status
		res.Reason = reason
	}

	c.results.Write(res)
	switch res.Status {
	case result.Valid:
		c.bar.IncValid()
	case result.Invalid:
		c.bar.IncInvalid()
	case result.Error:
		c.bar.IncError()
	case result.HostNotFound:
		c.bar.IncHostNotFound()
	}
}

func (c *Checker) tryLogin(cred Credential, info db.ServerInfo) (result.Status, string) {
	addr := fmt.Sprintf("%s:%d", info.Host, info.Port)
	var lastReason string
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(retryWait)
		}
		status, reason, isNetErr := c.doLogin(cred, addr, info.Port)
		if !isNetErr {
			return status, reason
		}
		lastReason = reason
	}
	return result.Error, lastReason
}

func (c *Checker) doLogin(cred Credential, addr string, port int) (status result.Status, reason string, isNetErr bool) {
	rawConn, err := c.dial(addr)
	if err != nil {
		return result.Error, "dial: " + err.Error(), true
	}
	_ = rawConn.SetDeadline(time.Now().Add(dialTimeout))

	var client *imapclient.Client
	if port == 143 {
		// Plain connect → read greeting → STARTTLS upgrade
		// imapclient.NewStartTLS handles both the greeting read and the STARTTLS handshake.
		// Defensive: even though the library closes rawConn on failure, a redundant
		// Close is idempotent and protects against future behavior changes.
		client, err = imapclient.NewStartTLS(rawConn, &imapclient.Options{TLSConfig: tlsCfg})
		if err != nil {
			_ = rawConn.Close()
			return result.Error, "starttls: " + err.Error(), true
		}
	} else {
		// Implicit TLS (port 993 and any other port from DB)
		tlsConn := tls.Client(rawConn, tlsCfg)
		if err := tlsConn.Handshake(); err != nil {
			_ = rawConn.Close()
			return result.Error, "tls: " + err.Error(), true
		}
		client = imapclient.New(tlsConn, nil)
	}
	defer client.Close()

	if err := client.Login(cred.User, cred.Pass).Wait(); err != nil {
		var imapErr *imap.Error
		if errors.As(err, &imapErr) {
			return result.Invalid, "", false // IMAP NO/BAD = wrong password, don't retry
		}
		return result.Error, "login: " + err.Error(), true
	}
	return result.Valid, "", false
}

// dial establishes a TCP connection to addr, optionally tunneled through an HTTP CONNECT proxy.
func (c *Checker) dial(addr string) (net.Conn, error) {
	proxyAddr := c.proxyPool.Next()
	dialer := &net.Dialer{Timeout: dialTimeout}

	if proxyAddr == "" {
		return dialer.Dial("tcp", addr)
	}

	// Connect to proxy
	conn, err := dialer.Dial("tcp", proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("proxy dial: %w", err)
	}
	// Bound the entire CONNECT handshake so a slow/silent proxy can't park
	// a worker forever. Cleared on success by the caller via SetDeadline.
	if err := conn.SetDeadline(time.Now().Add(dialTimeout)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("proxy SetDeadline: %w", err)
	}

	// Send HTTP CONNECT request
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.0\r\nHost: %s\r\n\r\n", addr, addr); err != nil {
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT write: %w", err)
	}

	// Read proxy response byte-by-byte to avoid over-buffering into the IMAP stream
	var hdr bytes.Buffer
	b := make([]byte, 1)
	for {
		if _, err := conn.Read(b); err != nil {
			conn.Close()
			return nil, fmt.Errorf("proxy CONNECT read: %w", err)
		}
		hdr.Write(b)
		if bytes.HasSuffix(hdr.Bytes(), []byte("\r\n\r\n")) {
			break
		}
		if hdr.Len() > connectMaxHdr {
			conn.Close()
			return nil, fmt.Errorf("proxy CONNECT response too large")
		}
	}

	// Check status line: HTTP/1.x SP 200 SP ...
	statusLine := hdr.Bytes()
	if idx := bytes.Index(statusLine, []byte("\r\n")); idx >= 0 {
		statusLine = statusLine[:idx]
	}
	if !statusLineIs2xx(statusLine) {
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT failed: %s", string(statusLine))
	}
	return conn, nil
}

// statusLineIs2xx returns true if line's space-delimited second field is in [200,300).
// Stricter than bytes.Contains("200") — rejects "HTTP/1.1 500 ... 200 ..." reasons.
func statusLineIs2xx(line []byte) bool {
	parts := bytes.SplitN(line, []byte(" "), 3)
	if len(parts) < 2 || len(parts[1]) != 3 {
		return false
	}
	return parts[1][0] == '2'
}
