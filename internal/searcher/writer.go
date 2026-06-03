package searcher

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var fileNames = [3]string{"found.txt", "not_found.txt", "error.txt"}

// Writer writes search results to 3 categorised output files.
type Writer struct {
	bufs     [3]*bufio.Writer
	mu       [3]sync.Mutex
	files    [3]*os.File
	writeErr atomic.Pointer[error]
}

// NewWriter creates outDir (if needed) and opens the 3 output files.
func NewWriter(outDir string) (*Writer, error) {
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

// WriteFound records an account where N matching emails were found.
func (w *Writer) WriteFound(user, pass string, n int) {
	w.mu[0].Lock()
	fmt.Fprintf(w.bufs[0], "%s:%s:%d\n", user, pass, n)
	w.mu[0].Unlock()
}

// WriteNotFound records an account with no matching emails.
func (w *Writer) WriteNotFound(user, pass string) {
	w.mu[1].Lock()
	fmt.Fprintf(w.bufs[1], "%s:%s\n", user, pass)
	w.mu[1].Unlock()
}

// WriteError records an account that could not be searched.
func (w *Writer) WriteError(user, pass, reason string) {
	w.mu[2].Lock()
	fmt.Fprintf(w.bufs[2], "%s:%s:%s\n", user, pass, sanitize(reason))
	w.mu[2].Unlock()
}

func sanitize(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func (w *Writer) recordErr(err error) {
	if err == nil {
		return
	}
	w.writeErr.CompareAndSwap(nil, &err)
}

// Flush flushes all buffers to disk.
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
// Returns a stop function that blocks until the goroutine exits.
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

// Close flushes and closes all files. Returns the first I/O error observed.
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
