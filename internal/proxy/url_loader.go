package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

const (
	fetchTimeout = 30 * time.Second
	maxBodySize  = 64 << 20 // 64 MiB safety cap on the response
)

// fetchURL retrieves the proxy list from url and returns the parsed entries.
// The HTTP client forces IPv4 because some providers' IP-allowlist auth only
// covers the IPv4 egress of this host.
func fetchURL(ctx context.Context, url string) ([]string, error) {
	tr := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2: false, // some providers prefer h1
	}
	// Force IPv4 — providers typically allowlist v4 only.
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		return d.DialContext(ctx, "tcp4", addr)
	}
	client := &http.Client{Timeout: fetchTimeout, Transport: tr}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "imap_checker/1.0")
	req.Header.Set("Accept", "text/plain")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http GET: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http status %d", resp.StatusCode)
	}
	return parseLines(io.LimitReader(resp.Body, maxBodySize))
}

// PollerStats reports the most recent poll outcome (for logging).
type PollerStats struct {
	LastSuccess time.Time
	LastCount   int
	LastErr     error
}

// Logger is the minimal logging surface used by the poller. Pass nil to skip.
type Logger interface {
	Printf(format string, args ...any)
}

// StartURLPoller fetches url synchronously into p once, then refreshes p
// every interval in the background. Failed refreshes are logged and leave
// the previous list in place. Returns a stop function that signals the
// goroutine and blocks until it exits.
//
// Returns the error from the initial fetch — if that fails the caller
// should abort (Phase 2 has no proxies to use).
func StartURLPoller(p *Pool, url string, interval time.Duration, logger Logger) (stop func(), err error) {
	ctx0, cancel0 := context.WithTimeout(context.Background(), fetchTimeout)
	list, err := fetchURL(ctx0, url)
	cancel0()
	if err != nil {
		return nil, fmt.Errorf("initial proxy fetch: %w", err)
	}
	p.SetProxies(list)
	logf(logger, "proxy poller: initial fetch ok, %d entries", len(list))

	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
				newList, err := fetchURL(ctx, url)
				cancel()
				if err != nil {
					logf(logger, "proxy poller: refresh failed (keeping %d old entries): %v", p.Len(), err)
					continue
				}
				p.SetProxies(newList)
				logf(logger, "proxy poller: refreshed, %d entries", len(newList))
			case <-done:
				return
			}
		}
	}()

	return func() {
		close(done)
		<-exited
	}, nil
}

func logf(l Logger, format string, args ...any) {
	if l != nil {
		l.Printf(format, args...)
	}
}
