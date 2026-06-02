package checker

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
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
	// dialTimeout bounds each dial AND, via SetDeadline, the subsequent
	// TLS handshake + IMAP LOGIN. 10 s rescues fast servers without
	// parking workers on dead proxies; healthy IMAP responds in < 1 s.
	dialTimeout = 10 * time.Second
	// tcpNet is "tcp4" so all callouts land on the IPv4 egress — required
	// by IP-allowlisted proxy providers, and avoids unpredictable v6 paths.
	tcpNet        = "tcp4"
	retryWait     = time.Second
	maxAttempts   = 2
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

// dial establishes a TCP connection to addr. When the pool is non-empty it
// tunnels through the next proxy using either HTTP CONNECT or SOCKS5
// depending on pool.Kind(). On empty pool, dials directly.
func (c *Checker) dial(addr string) (net.Conn, error) {
	proxyAddr := c.proxyPool.Next()
	dialer := &net.Dialer{Timeout: dialTimeout}

	if proxyAddr == "" {
		return dialer.Dial(tcpNet, addr)
	}

	conn, err := dialer.Dial(tcpNet, proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("proxy dial: %w", err)
	}
	// Bound the entire handshake so a slow/silent proxy can't park a worker
	// forever. Cleared on success by the caller via SetDeadline.
	if err := conn.SetDeadline(time.Now().Add(dialTimeout)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("proxy SetDeadline: %w", err)
	}

	switch c.proxyPool.Kind() {
	case proxy.KindSOCKS5:
		if err := socks5Handshake(conn, addr); err != nil {
			conn.Close()
			return nil, err
		}
	default: // KindHTTPConnect
		if err := httpConnectHandshake(conn, addr); err != nil {
			conn.Close()
			return nil, err
		}
	}
	return conn, nil
}

// httpConnectHandshake performs an HTTP/1.0 CONNECT tunnel handshake on conn.
// On success conn is ready to carry the tunneled bytes (TLS, then IMAP).
func httpConnectHandshake(conn net.Conn, addr string) error {
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.0\r\nHost: %s\r\n\r\n", addr, addr); err != nil {
		return fmt.Errorf("proxy CONNECT write: %w", err)
	}

	// Read response byte-by-byte to avoid over-buffering into the IMAP stream.
	var hdr bytes.Buffer
	b := make([]byte, 1)
	for {
		if _, err := conn.Read(b); err != nil {
			return fmt.Errorf("proxy CONNECT read: %w", err)
		}
		hdr.Write(b)
		if bytes.HasSuffix(hdr.Bytes(), []byte("\r\n\r\n")) {
			break
		}
		if hdr.Len() > connectMaxHdr {
			return fmt.Errorf("proxy CONNECT response too large")
		}
	}

	statusLine := hdr.Bytes()
	if idx := bytes.Index(statusLine, []byte("\r\n")); idx >= 0 {
		statusLine = statusLine[:idx]
	}
	if !statusLineIs2xx(statusLine) {
		return fmt.Errorf("proxy CONNECT failed: %s", string(statusLine))
	}
	return nil
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

// socks5Handshake performs an RFC 1928 SOCKS5 negotiation on conn with no
// authentication, then issues a CONNECT to addr ("host:port"). On success
// conn is positioned for the upstream protocol stream.
func socks5Handshake(conn net.Conn, addr string) error {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("socks5 split host:port: %w", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 0xFFFF {
		return fmt.Errorf("socks5 bad port %q", portStr)
	}
	if len(host) > 255 {
		return fmt.Errorf("socks5 host too long (%d)", len(host))
	}

	// Greeting: VER=5, NMETHODS=1, METHOD=0 (no auth).
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return fmt.Errorf("socks5 greet write: %w", err)
	}
	greetResp := make([]byte, 2)
	if _, err := io.ReadFull(conn, greetResp); err != nil {
		return fmt.Errorf("socks5 greet read: %w", err)
	}
	if greetResp[0] != 0x05 {
		return fmt.Errorf("socks5 bad version %#x", greetResp[0])
	}
	if greetResp[1] != 0x00 {
		return fmt.Errorf("socks5 unsupported auth method %#x", greetResp[1])
	}

	// CONNECT: VER=5, CMD=1 (connect), RSV=0, ATYP=3 (domain), len, host, port BE.
	req := make([]byte, 0, 7+len(host))
	req = append(req, 0x05, 0x01, 0x00, 0x03, byte(len(host)))
	req = append(req, host...)
	req = append(req, byte(port>>8), byte(port&0xFF))
	if _, err := conn.Write(req); err != nil {
		return fmt.Errorf("socks5 connect write: %w", err)
	}

	// Reply: VER, REP, RSV, ATYP, BND.ADDR, BND.PORT.
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		return fmt.Errorf("socks5 reply head: %w", err)
	}
	if head[0] != 0x05 {
		return fmt.Errorf("socks5 reply bad version %#x", head[0])
	}
	if head[1] != 0x00 {
		return fmt.Errorf("socks5 reply status %s", socks5StatusName(head[1]))
	}
	// Drain BND.ADDR + BND.PORT so the conn is positioned at the upstream payload.
	var addrLen int
	switch head[3] {
	case 0x01: // IPv4
		addrLen = 4
	case 0x03: // domain
		l := make([]byte, 1)
		if _, err := io.ReadFull(conn, l); err != nil {
			return fmt.Errorf("socks5 reply addr len: %w", err)
		}
		addrLen = int(l[0])
	case 0x04: // IPv6
		addrLen = 16
	default:
		return fmt.Errorf("socks5 reply unknown atyp %#x", head[3])
	}
	if _, err := io.CopyN(io.Discard, conn, int64(addrLen+2)); err != nil {
		return fmt.Errorf("socks5 reply addr+port: %w", err)
	}
	return nil
}

func socks5StatusName(b byte) string {
	switch b {
	case 0x01:
		return "general failure"
	case 0x02:
		return "not allowed by ruleset"
	case 0x03:
		return "network unreachable"
	case 0x04:
		return "host unreachable"
	case 0x05:
		return "connection refused"
	case 0x06:
		return "TTL expired"
	case 0x07:
		return "command not supported"
	case 0x08:
		return "address type not supported"
	default:
		return fmt.Sprintf("status %#x", b)
	}
}
