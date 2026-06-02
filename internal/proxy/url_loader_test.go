package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// errLogger captures Printf calls for assertions.
type errLogger struct {
	msgs atomic.Pointer[[]string]
}

func (l *errLogger) Printf(format string, args ...any) {
	cur := l.msgs.Load()
	var arr []string
	if cur != nil {
		arr = append(arr, *cur...)
	}
	arr = append(arr, fmt.Sprintf(format, args...))
	l.msgs.Store(&arr)
}

func newErrLogger() *errLogger {
	l := &errLogger{}
	empty := []string{}
	l.msgs.Store(&empty)
	return l
}

func (l *errLogger) all() []string { return *l.msgs.Load() }

func TestStartURLPoller_InitialFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintln(w, "1.1.1.1:1080")
		fmt.Fprintln(w, "2.2.2.2:1080")
		fmt.Fprintln(w, "  3.3.3.3:1080  ")
	}))
	defer srv.Close()

	p := New(KindSOCKS5)
	stop, err := StartURLPoller(p, srv.URL, time.Hour, nil)
	if err != nil {
		t.Fatalf("StartURLPoller: %v", err)
	}
	defer stop()

	if got := p.Len(); got != 3 {
		t.Errorf("Len()=%d, want 3", got)
	}
	if got := p.Next(); got != "1.1.1.1:1080" {
		t.Errorf("first=%q, want 1.1.1.1:1080", got)
	}
}

func TestStartURLPoller_InitialFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	p := New(KindSOCKS5)
	stop, err := StartURLPoller(p, srv.URL, time.Hour, nil)
	if err == nil {
		stop()
		t.Fatal("expected initial fetch error, got nil")
	}
	if p.Len() != 0 {
		t.Errorf("pool should remain empty on initial failure, got Len=%d", p.Len())
	}
}

func TestStartURLPoller_RefreshKeepsOldOnError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		switch n {
		case 1:
			fmt.Fprintln(w, "good1:1")
			fmt.Fprintln(w, "good2:2")
		default:
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	logger := newErrLogger()
	p := New(KindSOCKS5)
	stop, err := StartURLPoller(p, srv.URL, 80*time.Millisecond, logger)
	if err != nil {
		t.Fatalf("initial: %v", err)
	}
	defer stop()

	// Wait for at least one failed refresh.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if p.Len() != 2 {
		t.Errorf("pool drained on refresh error, Len=%d", p.Len())
	}
	if got := p.Next(); got != "good1:1" && got != "good2:2" {
		t.Errorf("Next()=%q not in original list", got)
	}

	found := false
	for _, m := range logger.all() {
		if len(m) > 0 && (m[:1] == "p") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected at least one log message, got %v", logger.all())
	}
}

func TestStartURLPoller_RefreshSwaps(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			fmt.Fprintln(w, "old:1")
		} else {
			fmt.Fprintln(w, "new1:1")
			fmt.Fprintln(w, "new2:2")
			fmt.Fprintln(w, "new3:3")
		}
	}))
	defer srv.Close()

	p := New(KindSOCKS5)
	stop, err := StartURLPoller(p, srv.URL, 80*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("initial: %v", err)
	}
	defer stop()

	if p.Len() != 1 {
		t.Fatalf("initial Len=%d, want 1", p.Len())
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p.Len() == 3 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("pool did not swap to new list within 2s, Len=%d", p.Len())
}
