package tgbot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type fakeOutput struct {
	mu      sync.Mutex
	uploads []string
	texts   []string
}

func (f *fakeOutput) UploadFile(_ context.Context, _, caption string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploads = append(f.uploads, caption)
	return nil
}
func (f *fakeOutput) SendText(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.texts = append(f.texts, text)
	return nil
}

func writeJob(id int64, name, contents string) Job {
	return Job{
		MessageID: id,
		ChannelID: 1,
		FileName:  name,
		Download: func(_ context.Context, dst string) error {
			return os.WriteFile(dst, []byte(contents), 0o644)
		},
	}
}

func newTestBot(t *testing.T, out Output, process ProcessFunc) *Bot {
	t.Helper()
	st := newTestState(t)
	return &Bot{
		state:   st,
		out:     out,
		process: process,
		workDir: t.TempDir(),
		jobs:    make(chan Job, jobsBufSize),
		ready:   make(chan readyJob, readyBufSize),
	}
}

// runJob is a test helper that runs both pipeline stages synchronously.
func runJob(b *Bot, job Job) {
	ctx := context.Background()
	b.downloadJob(ctx, job)
	// Drain ready channel synchronously.
	for {
		select {
		case rj := <-b.ready:
			b.processReady(ctx, rj)
		default:
			return
		}
	}
}

func TestProcessJobSuccessWithValid(t *testing.T) {
	out := &fakeOutput{}
	process := func(_ context.Context, _, outDir string) (Result, error) {
		vp := filepath.Join(outDir, "valid.txt")
		os.MkdirAll(outDir, 0o755)
		os.WriteFile(vp, []byte("a@x:1:s:993\nb@x:2:s:993\n"), 0o644)
		return Result{Total: 5, Valid: 2, ValidTxtPath: vp}, nil
	}
	b := newTestBot(t, out, process)

	runJob(b, writeJob(100, "creds.txt", "a@x:1\n"))

	if len(out.uploads) != 1 {
		t.Fatalf("uploads = %d, want 1", len(out.uploads))
	}
	has, _ := b.state.Has(100)
	if !has {
		t.Fatal("state missing message 100")
	}
}

func TestProcessJobZeroValidSendsTextOnly(t *testing.T) {
	out := &fakeOutput{}
	process := func(_ context.Context, _, outDir string) (Result, error) {
		os.MkdirAll(outDir, 0o755)
		vp := filepath.Join(outDir, "valid.txt")
		os.WriteFile(vp, nil, 0o644)
		return Result{Total: 3, Valid: 0, ValidTxtPath: vp}, nil
	}
	b := newTestBot(t, out, process)

	runJob(b, writeJob(101, "creds.txt", "x\n"))

	if len(out.uploads) != 0 {
		t.Errorf("uploads = %d, want 0 (no file when Valid==0)", len(out.uploads))
	}
	if len(out.texts) != 1 {
		t.Errorf("texts = %d, want 1 (caption only)", len(out.texts))
	}
}

func TestProcessJobDedup(t *testing.T) {
	out := &fakeOutput{}
	var calls int
	process := func(_ context.Context, _, outDir string) (Result, error) {
		calls++
		os.MkdirAll(outDir, 0o755)
		vp := filepath.Join(outDir, "valid.txt")
		os.WriteFile(vp, nil, 0o644)
		return Result{Total: 1, Valid: 0, ValidTxtPath: vp}, nil
	}
	b := newTestBot(t, out, process)

	runJob(b, writeJob(102, "c.txt", "x\n"))
	runJob(b, writeJob(102, "c.txt", "x\n")) // duplicate

	if calls != 1 {
		t.Fatalf("process called %d times, want 1 (dedup)", calls)
	}
}

func TestProcessJobErrorMarksAndNotifies(t *testing.T) {
	out := &fakeOutput{}
	process := func(_ context.Context, _, _ string) (Result, error) {
		return Result{}, errors.New("boom")
	}
	b := newTestBot(t, out, process)

	runJob(b, writeJob(103, "c.txt", "x\n"))

	ids, _ := b.state.IncompleteIDs()
	for _, id := range ids {
		if id == 103 {
			t.Fatal("103 should be marked error (-2), not left incomplete (-1)")
		}
	}
	if len(out.texts) != 1 {
		t.Errorf("expected 1 error notification, got %d", len(out.texts))
	}
}
