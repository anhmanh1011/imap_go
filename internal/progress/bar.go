package progress

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// Bar tracks check counts and renders a live progress line to stderr.
type Bar struct {
	total        int64
	valid        atomic.Int64
	invalid      atomic.Int64
	errCount     atomic.Int64
	hostNotFound atomic.Int64
	processed    atomic.Int64
}

// New creates a Bar for a run with the given total credential count.
func New(total int64) *Bar {
	return &Bar{total: total}
}

func (b *Bar) IncValid()        { b.valid.Add(1); b.processed.Add(1) }
func (b *Bar) IncInvalid()      { b.invalid.Add(1); b.processed.Add(1) }
func (b *Bar) IncError()        { b.errCount.Add(1); b.processed.Add(1) }
func (b *Bar) IncHostNotFound() { b.hostNotFound.Add(1); b.processed.Add(1) }

func (b *Bar) render(speed int64) string {
	proc := b.processed.Load()
	total := b.total
	if total == 0 {
		total = 1
	}
	pct := float64(proc) / float64(total) * 100
	const width = 20
	filled := int(float64(width) * float64(proc) / float64(total))
	if filled > width {
		filled = width
	}
	bar := strings.Repeat("=", filled) + ">" + strings.Repeat(" ", width-filled)
	return fmt.Sprintf("\r[%s] %d/%d (%.0f%%) | valid: %d | invalid: %d | error: %d | hnf: %d | %d acc/s",
		bar, proc, b.total, pct,
		b.valid.Load(), b.invalid.Load(), b.errCount.Load(), b.hostNotFound.Load(),
		speed,
	)
}

// Start renders the progress bar every 200ms until the returned stop function is called.
// The stop function prints the final state and blocks until the renderer goroutine exits.
func (b *Bar) Start() func() {
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		var lastProc int64
		for {
			select {
			case <-ticker.C:
				proc := b.processed.Load()
				speed := (proc - lastProc) * 5 // 200ms ticks × 5 = 1 second
				lastProc = proc
				fmt.Fprint(os.Stderr, b.render(speed))
			case <-done:
				fmt.Fprintln(os.Stderr, b.render(0))
				return
			}
		}
	}()
	return func() {
		close(done)
		<-exited
	}
}
