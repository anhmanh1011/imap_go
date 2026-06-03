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
	start        time.Time // set in Start(); avg CPM / ETA are computed from this
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

	// CPM: instant = speed × 60. avg = lifetime processed / elapsed seconds × 60.
	// ETA derived from avg CPM so a transient stall doesn't blow up the estimate.
	cpm := speed * 60
	var avgCPM int64
	eta := "--"
	if !b.start.IsZero() {
		elapsed := time.Since(b.start).Seconds()
		if elapsed > 0 {
			avgCPM = int64(float64(proc) / elapsed * 60)
		}
		if avgCPM > 0 && proc < b.total {
			minsLeft := float64(b.total-proc) / float64(avgCPM)
			eta = formatETA(minsLeft)
		}
	}

	return fmt.Sprintf("\r[%s] %d/%d (%.0f%%) | valid: %d | invalid: %d | error: %d | hnf: %d | %d acc/s | CPM %d avg %d | ETA %s",
		bar, proc, b.total, pct,
		b.valid.Load(), b.invalid.Load(), b.errCount.Load(), b.hostNotFound.Load(),
		speed, cpm, avgCPM, eta,
	)
}

// formatETA renders minutes as the most informative compact human form.
//   <1   -> "<1m"
//   <60  -> "Nm"
//   <1440 -> "HhMMm"
//   else -> "DdHHhMMm"
func formatETA(minutes float64) string {
	if minutes < 1 {
		return "<1m"
	}
	total := int64(minutes)
	d := total / (24 * 60)
	h := (total / 60) % 24
	m := total % 60
	switch {
	case d > 0:
		return fmt.Sprintf("%dd%02dh%02dm", d, h, m)
	case h > 0:
		return fmt.Sprintf("%dh%02dm", h, m)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

// Start renders the progress bar every 200ms until the returned stop function is called.
// The stop function prints the final state and blocks until the renderer goroutine exits.
func (b *Bar) Start() func() {
	b.start = time.Now()
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
