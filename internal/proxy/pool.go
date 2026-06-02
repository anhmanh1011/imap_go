package proxy

import (
	"bufio"
	"io"
	"os"
	"strings"
	"sync/atomic"
)

// Kind identifies the tunneling protocol of the proxies in a Pool.
// The checker picks its dial path based on this.
type Kind int

const (
	KindHTTPConnect Kind = iota // "ip:port" speaks HTTP CONNECT
	KindSOCKS5                  // "ip:port" speaks SOCKS5 (RFC 1928, no auth)
)

// Pool is a lock-free round-robin proxy pool whose backing list can be
// hot-swapped at runtime (see SetProxies / StartURLPoller).
type Pool struct {
	kind    Kind
	current atomic.Pointer[[]string]
	idx     atomic.Uint64
}

// New creates an empty Pool of the given kind. Callers populate it via
// SetProxies, LoadFileInto, or StartURLPoller.
func New(kind Kind) *Pool {
	p := &Pool{kind: kind}
	empty := []string{}
	p.current.Store(&empty)
	return p
}

// Kind returns the tunneling protocol used by entries in this pool.
func (p *Pool) Kind() Kind { return p.kind }

// SetProxies atomically swaps the proxy list. The round-robin counter is
// left untouched so an in-flight Next() call observing the old slice is
// still safe; the next call will see the new slice modulo its length.
func (p *Pool) SetProxies(list []string) {
	clean := make([]string, 0, len(list))
	for _, s := range list {
		if s = strings.TrimSpace(s); s != "" {
			clean = append(clean, s)
		}
	}
	p.current.Store(&clean)
}

// LoadFile reads a file of "ip:port" entries (one per line, blank lines
// ignored). Returns an HTTP-CONNECT Pool. Returns an empty pool without
// error when path is "".
func LoadFile(path string) (*Pool, error) {
	p := New(KindHTTPConnect)
	if path == "" {
		return p, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	list, err := parseLines(f)
	if err != nil {
		return nil, err
	}
	p.SetProxies(list)
	return p, nil
}

// parseLines reads "ip:port" entries from r, trimming whitespace and
// dropping blanks. Used by both file and URL loaders.
func parseLines(r io.Reader) ([]string, error) {
	var out []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			out = append(out, line)
		}
	}
	return out, sc.Err()
}

// Next returns the next proxy address in round-robin order, or "" when
// the pool is empty. Lock-free: a snapshot pointer plus atomic counter.
func (p *Pool) Next() string {
	list := *p.current.Load()
	n := uint64(len(list))
	if n == 0 {
		return ""
	}
	return list[(p.idx.Add(1)-1)%n]
}

// Len returns the current number of proxies. Reads the atomic snapshot.
func (p *Pool) Len() int {
	return len(*p.current.Load())
}
