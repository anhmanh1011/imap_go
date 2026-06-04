package tgbot

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	downloaderCount = 3  // concurrent Telegram downloads
	jobsBufSize     = 100 // incoming job queue
	readyBufSize    = 5   // downloaded-and-waiting-for-IMAP queue
)

// Job is one downloadable .txt message from the input channel.
type Job struct {
	MessageID int64
	ChannelID int64
	FileName  string
	Download  func(ctx context.Context, dst string) error
}

// readyJob is a Job whose file has already been downloaded locally.
type readyJob struct {
	Job
	inputPath string
	runDir    string
}

// Output publishes results to the output channel.
type Output interface {
	UploadFile(ctx context.Context, path, caption string) error
	SendText(ctx context.Context, text string) error
}

// ProcessFunc runs the checker pipeline for a downloaded input file.
type ProcessFunc func(ctx context.Context, inputPath, outDir string) (Result, error)

// Bot runs a two-stage pipeline:
//   Stage 1 — downloaderCount goroutines drain jobs and download files immediately.
//   Stage 2 — one processor goroutine runs IMAP checks sequentially.
//
// Decoupling the stages means new files are downloaded while IMAP is busy,
// so no file waits idle on disk for a prior check to finish.
type Bot struct {
	state   *State
	out     Output
	process ProcessFunc
	workDir string
	jobs    chan Job     // stage-1 input  (backfill + realtime)
	ready   chan readyJob // stage-2 input  (downloaded, awaiting IMAP)
}

// NewBot creates a Bot with buffered channels for both pipeline stages.
func NewBot(state *State, out Output, process ProcessFunc, workDir string) *Bot {
	return &Bot{
		state:   state,
		out:     out,
		process: process,
		workDir: workDir,
		jobs:    make(chan Job, jobsBufSize),
		ready:   make(chan readyJob, readyBufSize),
	}
}

// Jobs returns the send-only jobs channel (backfill + realtime enqueue here).
func (b *Bot) Jobs() chan<- Job { return b.jobs }

// Run starts the full pipeline and blocks until ctx is cancelled.
// Stage 1: downloaderCount goroutines → download files as they arrive.
// Stage 2: one goroutine → IMAP check sequentially.
func (b *Bot) Run(ctx context.Context) {
	var wg sync.WaitGroup

	// Stage 1 — parallel downloaders
	for i := 0; i < downloaderCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job, ok := <-b.jobs:
					if !ok {
						return
					}
					b.downloadJob(ctx, job)
				}
			}
		}()
	}

	// Stage 2 — sequential IMAP processor
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case rj, ok := <-b.ready:
				if !ok {
					return
				}
				b.processReady(ctx, rj)
			}
		}
	}()

	wg.Wait()
}

// downloadJob (Stage 1): dedup, download, push to ready channel.
// Does NOT block on IMAP — returns as soon as the file is on disk.
func (b *Bot) downloadJob(ctx context.Context, job Job) {
	has, err := b.state.Has(job.MessageID)
	if err != nil {
		log.Printf("tgbot: state.Has(%d): %v", job.MessageID, err)
		return
	}
	if has {
		return
	}

	runDir := filepath.Join(b.workDir, fmt.Sprintf("%d", job.MessageID))
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		log.Printf("tgbot: mkdir %s: %v", runDir, err)
		return
	}

	inputPath := filepath.Join(runDir, "input.txt")
	dlStart := time.Now()
	if err := job.Download(ctx, inputPath); err != nil {
		log.Printf("tgbot: download msg#%d: %v", job.MessageID, err)
		os.RemoveAll(runDir)
		return
	}
	if fi, err := os.Stat(inputPath); err == nil {
		elapsed := time.Since(dlStart).Seconds()
		sizeMB := float64(fi.Size()) / 1024 / 1024
		speed := ""
		if elapsed > 0 {
			speed = fmt.Sprintf("%.1f MB/s", sizeMB/elapsed)
		}
		log.Printf("tgbot: downloaded msg#%d %q → %.2f MB in %.1fs (%s)",
			job.MessageID, job.FileName, sizeMB, elapsed, speed)
	}

	// Push to IMAP stage — non-blocking attempt first, then blocking.
	select {
	case b.ready <- readyJob{Job: job, inputPath: inputPath, runDir: runDir}:
	case <-ctx.Done():
		os.RemoveAll(runDir)
	}
}

// processReady (Stage 2): insert state, run IMAP check, upload result.
func (b *Bot) processReady(ctx context.Context, rj readyJob) {
	defer os.RemoveAll(rj.runDir)

	// Double-check dedup (another downloader may have raced for the same ID).
	has, err := b.state.Has(rj.MessageID)
	if err != nil {
		log.Printf("tgbot: state.Has(%d): %v", rj.MessageID, err)
		return
	}
	if has {
		return
	}

	if err := b.state.Insert(rj.MessageID, rj.ChannelID); err != nil {
		log.Printf("tgbot: state.Insert(%d): %v", rj.MessageID, err)
		return
	}

	outDir := filepath.Join(rj.runDir, "output")
	res, err := b.process(ctx, rj.inputPath, outDir)
	if err != nil {
		log.Printf("tgbot: process msg#%d: %v", rj.MessageID, err)
		_ = b.state.MarkError(rj.MessageID)
		_ = b.out.SendText(ctx, fmt.Sprintf("❌ Failed: msg#%d (%s): %v", rj.MessageID, rj.FileName, err))
		return
	}

	caption := fmt.Sprintf("✅ Done: msg#%d | total: %d | valid: %d\nFile: %s",
		rj.MessageID, res.Total, res.Valid, rj.FileName)

	if res.Valid > 0 {
		if err := b.out.UploadFile(ctx, res.ValidTxtPath, caption); err != nil {
			log.Printf("tgbot: upload msg#%d: %v", rj.MessageID, err)
		}
	} else {
		if err := b.out.SendText(ctx, caption); err != nil {
			log.Printf("tgbot: sendtext msg#%d: %v", rj.MessageID, err)
		}
	}

	if err := b.state.Complete(rj.MessageID, res.Total, res.Valid); err != nil {
		log.Printf("tgbot: state.Complete(%d): %v", rj.MessageID, err)
	}
}
