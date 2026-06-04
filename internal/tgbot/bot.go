package tgbot

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// Job is one downloadable .txt message pulled from the input channel.
type Job struct {
	MessageID int64
	ChannelID int64
	FileName  string
	Download  func(ctx context.Context, dst string) error
}

// Output publishes results to the output channel.
type Output interface {
	UploadFile(ctx context.Context, path, caption string) error
	SendText(ctx context.Context, text string) error
}

// ProcessFunc runs the checker pipeline for a downloaded input file.
type ProcessFunc func(ctx context.Context, inputPath, outDir string) (Result, error)

// Bot consumes Jobs sequentially: only one checker run happens at a time.
type Bot struct {
	state   *State
	out     Output
	process ProcessFunc
	workDir string
	jobs    chan Job
}

// NewBot builds a Bot with a buffered job channel (cap 10).
func NewBot(state *State, out Output, process ProcessFunc, workDir string) *Bot {
	return &Bot{
		state:   state,
		out:     out,
		process: process,
		workDir: workDir,
		jobs:    make(chan Job, 10),
	}
}

// Jobs returns the send-only channel callers enqueue into.
func (b *Bot) Jobs() chan<- Job { return b.jobs }

// Run drains the job channel until ctx is cancelled or channel is closed.
func (b *Bot) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-b.jobs:
			if !ok {
				return
			}
			b.processJob(ctx, job)
		}
	}
}

// processJob handles one Job end to end.
func (b *Bot) processJob(ctx context.Context, job Job) {
	has, err := b.state.Has(job.MessageID)
	if err != nil {
		log.Printf("tgbot: state.Has(%d): %v", job.MessageID, err)
		return
	}
	if has {
		return
	}

	runDir := filepath.Join(b.workDir, fmt.Sprintf("%d", job.MessageID))
	outDir := filepath.Join(runDir, "output")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		log.Printf("tgbot: mkdir %s: %v", runDir, err)
		return
	}
	defer os.RemoveAll(runDir)

	inputPath := filepath.Join(runDir, "input.txt")
	dlStart := time.Now()
	if err := job.Download(ctx, inputPath); err != nil {
		log.Printf("tgbot: download msg#%d: %v", job.MessageID, err)
		return
	}
	if fi, err := os.Stat(inputPath); err == nil {
		elapsed := time.Since(dlStart).Seconds()
		sizeMB := float64(fi.Size()) / 1024 / 1024
		var speed string
		if elapsed > 0 {
			speed = fmt.Sprintf("%.1f MB/s", sizeMB/elapsed)
		}
		log.Printf("tgbot: downloaded msg#%d %q → %.2f MB in %.1fs (%s)",
			job.MessageID, job.FileName, sizeMB, elapsed, speed)
	}

	if err := b.state.Insert(job.MessageID, job.ChannelID); err != nil {
		log.Printf("tgbot: state.Insert(%d): %v", job.MessageID, err)
		return
	}

	res, err := b.process(ctx, inputPath, outDir)
	if err != nil {
		log.Printf("tgbot: process msg#%d: %v", job.MessageID, err)
		_ = b.state.MarkError(job.MessageID)
		_ = b.out.SendText(ctx, fmt.Sprintf("❌ Failed: msg#%d (%s): %v", job.MessageID, job.FileName, err))
		return
	}

	caption := fmt.Sprintf("✅ Done: msg#%d | total: %d | valid: %d\nFile: %s",
		job.MessageID, res.Total, res.Valid, job.FileName)

	if res.Valid > 0 {
		if err := b.out.UploadFile(ctx, res.ValidTxtPath, caption); err != nil {
			log.Printf("tgbot: upload msg#%d: %v", job.MessageID, err)
		}
	} else {
		if err := b.out.SendText(ctx, caption); err != nil {
			log.Printf("tgbot: sendtext msg#%d: %v", job.MessageID, err)
		}
	}

	if err := b.state.Complete(job.MessageID, res.Total, res.Valid); err != nil {
		log.Printf("tgbot: state.Complete(%d): %v", job.MessageID, err)
	}
}
