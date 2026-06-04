package result

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// Status classifies the outcome of one IMAP login attempt.
type Status int

const (
	Valid        Status = iota // IMAP LOGIN returned OK
	Invalid                    // IMAP LOGIN returned NO or BAD
	Error                      // Network/TLS failure after 1 retry
	HostNotFound               // Not in DB and fallback imap.<domain>:993 also failed
)

// Result is the outcome of checking one credential pair.
type Result struct {
	User   string
	Pass   string
	Status Status
	Reason string // set for Error status only
	Server string // set for Valid status
	Port   int    // set for Valid status
}

var fileNames = [4]string{"valid.txt", "invalid.txt", "error.txt", "host_not_found.txt"}

// Writer writes results to 4 categorized, buffered output files.
type Writer struct {
	bufs    [4]*bufio.Writer
	mu      [4]sync.Mutex
	files   [4]*os.File
	writeErr atomic.Pointer[error] // first I/O error observed by Flush, surfaced on Close
}

// New creates outDir (if needed) and opens all 4 output files.
func New(outDir string) (*Writer, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	w := &Writer{}
	for i, name := range fileNames {
		f, err := os.Create(filepath.Join(outDir, name))
		if err != nil {
			return nil, err
		}
		w.files[i] = f
		w.bufs[i] = bufio.NewWriterSize(f, 4*1024)
	}
	return w, nil
}

// sanitizeReason strips CR/LF/colons so a hostile error string can't break the
// "user:pass:reason\n" line format in error.txt. Capped at 200 bytes.
func sanitizeReason(s string) string {
	const maxLen = 200
	if len(s) > maxLen {
		s = s[:maxLen]
	}
	r := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\r', '\n':
			r = append(r, ' ')
		default:
			r = append(r, s[i])
		}
	}
	return string(r)
}

// Write routes r to the correct output file. Safe for concurrent use.
func (w *Writer) Write(r Result) {
	idx := int(r.Status)
	w.mu[idx].Lock()
	switch r.Status {
	case Valid:
		fmt.Fprintf(w.bufs[idx], "%s:%s:%s:%d\n", r.User, r.Pass, r.Server, r.Port)
	case Error:
		fmt.Fprintf(w.bufs[idx], "%s:%s:%s\n", r.User, r.Pass, sanitizeReason(r.Reason))
	default:
		fmt.Fprintf(w.bufs[idx], "%s:%s\n", r.User, r.Pass)
	}
	w.mu[idx].Unlock()
}

// recordErr stores err as the first observed I/O error, if none was set before.
func (w *Writer) recordErr(err error) {
	if err == nil {
		return
	}
	w.writeErr.CompareAndSwap(nil, &err)
}

// Flush flushes all 4 buffers to disk. Errors are recorded and surfaced on Close.
func (w *Writer) Flush() {
	for i := range w.bufs {
		w.mu[i].Lock()
		if err := w.bufs[i].Flush(); err != nil {
			w.recordErr(err)
		}
		w.mu[i].Unlock()
	}
}

// StartAutoFlush starts a background goroutine that flushes every 100ms.
// Returns a stop function that blocks until the goroutine has fully exited —
// callers can safely call Close() after the stop function returns.
func (w *Writer) StartAutoFlush() func() {
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				w.Flush()
			case <-done:
				return
			}
		}
	}()
	return func() {
		close(done)
		<-exited
	}
}

// Close flushes all buffers and closes all files. Returns the first I/O error
// observed during the run (flush or close); nil if all writes landed cleanly.
func (w *Writer) Close() error {
	w.Flush()
	var errs []error
	for i := range w.files {
		w.mu[i].Lock()
		if err := w.files[i].Close(); err != nil {
			errs = append(errs, err)
		}
		w.mu[i].Unlock()
	}
	if p := w.writeErr.Load(); p != nil {
		errs = append([]error{*p}, errs...)
	}
	return errors.Join(errs...)
}
