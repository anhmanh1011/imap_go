package result

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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
}

var fileNames = [4]string{"valid.txt", "invalid.txt", "error.txt", "host_not_found.txt"}

// Writer writes results to 4 categorized, buffered output files.
type Writer struct {
	bufs  [4]*bufio.Writer
	mu    [4]sync.Mutex
	files [4]*os.File
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

// Write routes r to the correct output file. Safe for concurrent use.
func (w *Writer) Write(r Result) {
	idx := int(r.Status)
	w.mu[idx].Lock()
	if r.Status == Error {
		fmt.Fprintf(w.bufs[idx], "%s:%s:%s\n", r.User, r.Pass, r.Reason)
	} else {
		fmt.Fprintf(w.bufs[idx], "%s:%s\n", r.User, r.Pass)
	}
	w.mu[idx].Unlock()
}

// Flush flushes all 4 buffers to disk.
func (w *Writer) Flush() {
	for i := range w.bufs {
		w.mu[i].Lock()
		w.bufs[i].Flush() //nolint:errcheck
		w.mu[i].Unlock()
	}
}

// StartAutoFlush starts a background goroutine that flushes every 100ms.
// Returns a stop function — call it before Close().
func (w *Writer) StartAutoFlush() func() {
	done := make(chan struct{})
	go func() {
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
	return func() { close(done) }
}

// Close flushes all buffers and closes all files.
func (w *Writer) Close() error {
	w.Flush()
	for _, f := range w.files {
		f.Close()
	}
	return nil
}
