package proxy

import (
	"bufio"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

// Kind identifies the tunneling protocol of the proxies in a Pool.
// The checker picks its dial path based on this.
type Kind int

const (
	KindHTTPConnect Kind = iota // "ip:port" speaks HTTP CONNECT
	KindSOCKS5                  // "ip:port" speaks SOCKS5 (RFC 1928, no auth)
)

const (
	// evictThreshold: after this many cumulative failures within one
	// rotation window, a proxy is skipped by Next() until the next
	// SetProxies refresh resets the counters.
	evictThreshold int32 = 3
	// maxProbe: how many round-robin steps Next() may skip looking for a
	// non-evicted entry before giving up and returning the last candidate.
	// 8 keeps worst-case cost bounded even when most of the pool is dead.
	maxProbe = 8
)

// Pool is a lock-free round-robin proxy pool whose backing list can be
// hot-swapped at runtime (see SetProxies / StartURLPoller). Failing proxies
// can be temporarily evicted via MarkFailed; eviction state is reset on the
// next SetProxies call.
type Pool struct {
	kind    Kind
	current atomic.Pointer[[]string]
	idx     atomic.Uint64
	// fails maps proxy addr -> *atomic.Int32 (failure count). A pointer
	// to sync.Map so SetProxies can swap in a fresh empty map atomically
	// (in-flight MarkFailed calls on the old map are harmless — the
	// reads in Next() see the new map).
	fails atomic.Pointer[sync.Map]
}

// New creates an empty Pool of the given kind. Callers populate it via
// SetProxies, LoadFile, or StartURLPoller.
func New(kind Kind) *Pool {
	p := &Pool{kind: kind}
	empty := []string{}
	p.current.Store(&empty)
	p.fails.Store(&sync.Map{})
	return p
}

// Kind returns the tunneling protocol used by entries in this pool.
func (p *Pool) Kind() Kind { return p.kind }

// SetProxies atomically swaps the proxy list AND resets the failure
// counters. Round-robin index is preserved so an in-flight Next() call
// observing the old slice is still safe.
func (p *Pool) SetProxies(list []string) {
	clean := make([]string, 0, len(list))
	for _, s := range list {
		if s = strings.TrimSpace(s); s != "" {
			clean = append(clean, s)
		}
	}
	p.current.Store(&clean)
	p.fails.Store(&sync.Map{})
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

// Next returns the next proxy address in round-robin order, skipping any
// addresses that have reached evictThreshold failures. Probes up to
// maxProbe positions; if all probes hit evicted entries (degenerate pool)
// returns the last candidate so the caller still gets a string.
// Returns "" when the pool is empty.
func (p *Pool) Next() string {
	list := *p.current.Load()
	n := uint64(len(list))
	if n == 0 {
		return ""
	}
	var cand string
	for i := 0; i < maxProbe; i++ {
		cand = list[(p.idx.Add(1)-1)%n]
		if !p.isEvicted(cand) {
			return cand
		}
	}
	return cand
}

// MarkFailed records a failure for addr. Once evictThreshold failures are
// reached, the entry will be skipped by Next() until SetProxies resets.
func (p *Pool) MarkFailed(addr string) {
	if addr == "" {
		return
	}
	m := p.fails.Load()
	v, _ := m.LoadOrStore(addr, new(atomic.Int32))
	v.(*atomic.Int32).Add(1)
}

// isEvicted reports whether addr has accumulated enough failures to be
// skipped. Reads-only, lock-free via sync.Map.Load.
func (p *Pool) isEvicted(addr string) bool {
	m := p.fails.Load()
	v, ok := m.Load(addr)
	if !ok {
		return false
	}
	return v.(*atomic.Int32).Load() >= evictThreshold
}

// Evicted returns the current count of distinct addresses that have hit
// the eviction threshold. Useful for telemetry/logging.
func (p *Pool) Evicted() int {
	var count int
	p.fails.Load().Range(func(_, v any) bool {
		if v.(*atomic.Int32).Load() >= evictThreshold {
			count++
		}
		return true
	})
	return count
}

// Len returns the current number of proxies. Reads the atomic snapshot.
func (p *Pool) Len() int {
	return len(*p.current.Load())
}
