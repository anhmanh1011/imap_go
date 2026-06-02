package proxy

import (
	"bufio"
	"os"
	"strings"
	"sync/atomic"
)

// Pool is a lock-free round-robin HTTP proxy pool.
type Pool struct {
	proxies []string
	idx     atomic.Uint64
}

// LoadFile reads a file of "ip:port" proxy entries (one per line, blank lines ignored).
// Returns an empty pool without error when path is "".
func LoadFile(path string) (*Pool, error) {
	if path == "" {
		return &Pool{}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var proxies []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			proxies = append(proxies, line)
		}
	}
	return &Pool{proxies: proxies}, sc.Err()
}

// Next returns the next proxy address in round-robin order.
// Returns "" when the pool is empty (caller should dial directly).
func (p *Pool) Next() string {
	if len(p.proxies) == 0 {
		return ""
	}
	i := p.idx.Add(1) - 1
	return p.proxies[i%uint64(len(p.proxies))]
}

// Len returns the number of proxies loaded.
func (p *Pool) Len() int { return len(p.proxies) }
