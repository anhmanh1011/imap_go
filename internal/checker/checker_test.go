package checker

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestParseLine(t *testing.T) {
	tests := []struct {
		line       string
		wantUser   string
		wantPass   string
		wantDomain string
		wantOK     bool
	}{
		{"user@gmail.com:secret", "user@gmail.com", "secret", "gmail.com", true},
		{"user@example.com:p:a:s:s", "user@example.com", "p:a:s:s", "example.com", true},
		{"https://example.com:user@example.com:p:a:s:s", "user@example.com", "p:a:s:s", "example.com", true},
		{"com.example.app:user@example.com:secret", "user@example.com", "secret", "example.com", true},
		{"https://example.com:plainuser:user@example.com", "", "", "", false},
		{"user@domain.com:", "user@domain.com", "", "domain.com", true},
		{"noatsign:pass", "", "", "", false},
		{":pass", "", "", "", false},
		{"", "", "", "", false},
	}
	for _, tt := range tests {
		user, pass, domain, ok := parseLine(tt.line)
		if ok != tt.wantOK {
			t.Errorf("parseLine(%q): ok=%v want %v", tt.line, ok, tt.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if user != tt.wantUser || pass != tt.wantPass || domain != tt.wantDomain {
			t.Errorf("parseLine(%q) = (%q,%q,%q), want (%q,%q,%q)",
				tt.line, user, pass, domain, tt.wantUser, tt.wantPass, tt.wantDomain)
		}
	}
}

func TestUniqueDomains(t *testing.T) {
	creds := []Credential{
		{Domain: "gmail.com"},
		{Domain: "gmail.com"},
		{Domain: "yahoo.com"},
	}
	got := UniqueDomains(creds)
	if len(got) != 2 {
		t.Errorf("len=%d, want 2", len(got))
	}
}

// runSOCKS5Server is a minimal RFC 1928 server for testing. It accepts one
// no-auth client, sends a no-auth greeting reply, parses one CONNECT with
// ATYP=domain, sends a successful reply, then echoes the rest until close.
// Returns the listen address and a stop func.
func runSOCKS5Server(t *testing.T, status byte) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))

		// Greeting
		buf := make([]byte, 3)
		if _, err := io.ReadFull(c, buf); err != nil {
			return
		}
		if buf[0] != 0x05 {
			return
		}
		c.Write([]byte{0x05, 0x00})

		// CONNECT request head
		head := make([]byte, 4)
		if _, err := io.ReadFull(c, head); err != nil {
			return
		}
		switch head[3] {
		case 0x03:
			l := make([]byte, 1)
			if _, err := io.ReadFull(c, l); err != nil {
				return
			}
			rest := make([]byte, int(l[0])+2)
			if _, err := io.ReadFull(c, rest); err != nil {
				return
			}
		case 0x01:
			io.CopyN(io.Discard, c, 6)
		case 0x04:
			io.CopyN(io.Discard, c, 18)
		}

		// Reply: VER, REP, RSV, ATYP=1 (IPv4), 0.0.0.0:0
		c.Write([]byte{0x05, status, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

		// Idle until client closes.
		io.Copy(io.Discard, c)
	}()
	return ln.Addr().String(), func() {
		ln.Close()
		<-done
	}
}

func TestSOCKS5Handshake_Success(t *testing.T) {
	addr, stop := runSOCKS5Server(t, 0x00)
	defer stop()

	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	if err := socks5Handshake(conn, "imap.gmail.com:993", "", ""); err != nil {
		t.Errorf("socks5Handshake: %v", err)
	}
}

func TestSOCKS5Handshake_RefusedStatus(t *testing.T) {
	addr, stop := runSOCKS5Server(t, 0x05) // connection refused
	defer stop()

	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	err = socks5Handshake(conn, "imap.gmail.com:993", "", "")
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("expected 'connection refused' status, got %v", err)
	}
}

func TestSOCKS5StatusName(t *testing.T) {
	cases := map[byte]string{
		0x01: "general failure",
		0x02: "not allowed by ruleset",
		0x05: "connection refused",
		0xFF: "status 0xff",
	}
	for b, want := range cases {
		if got := socks5StatusName(b); got != want {
			t.Errorf("socks5StatusName(%#x) = %q, want %q", b, got, want)
		}
	}
}

func TestStatusLineIs2xx(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"HTTP/1.1 200 OK", true},
		{"HTTP/1.0 204 No Content", true},
		{"HTTP/1.1 500 Server Error 200 maybe", false},
		{"HTTP/1.1 407 Auth Required", false},
		{"garbage", false},
		{"HTTP/1.1 2 short", false},
	}
	for _, c := range cases {
		if got := statusLineIs2xx([]byte(c.line)); got != c.want {
			t.Errorf("statusLineIs2xx(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}
