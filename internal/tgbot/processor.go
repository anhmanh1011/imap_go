package tgbot

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"sync"

	"imap_checker/internal/checker"
	"imap_checker/internal/db"
	"imap_checker/internal/progress"
	"imap_checker/internal/proxy"
	"imap_checker/internal/result"
)

// Result is the outcome of processing one input file.
type Result struct {
	Total        int    // credentials checked (after blocklist filter)
	Valid        int    // lines in valid.txt
	ValidTxtPath string // path to the produced valid.txt
}

// Process runs the full checker pipeline for a single local input file and
// writes the 4 result files into outDir. It knows nothing about Telegram.
// pool is shared/read-only (built once at startup); Process never starts or
// stops a proxy poller. The progress bar is created but not started, so it
// produces no terminal output.
func Process(ctx context.Context, workers int, dbPath string, pool *proxy.Pool, inputPath, outDir string) (Result, error) {
	creds, err := checker.ParseFile(inputPath)
	if err != nil {
		return Result{}, err
	}
	creds, _ = checker.FilterBlocked(creds)

	domains := checker.UniqueDomains(creds)
	domainMap, err := db.BatchLookup(dbPath, domains)
	if err != nil {
		return Result{}, err
	}

	writer, err := result.New(outDir)
	if err != nil {
		return Result{}, err
	}
	stopFlush := writer.StartAutoFlush()

	bar := progress.New(int64(len(creds)))
	chk := checker.New(domainMap, pool, writer, bar)

	credChan := make(chan checker.Credential, workers*2)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for cred := range credChan {
				chk.Check(cred)
			}
		}()
	}
	for _, c := range creds {
		select {
		case <-ctx.Done():
		default:
		}
		credChan <- c
	}
	close(credChan)
	wg.Wait()

	stopFlush()
	closeErr := writer.Close()

	validPath := filepath.Join(outDir, "valid.txt")
	valid, err := countFileLines(validPath)
	if err != nil {
		return Result{}, err
	}
	return Result{Total: len(creds), Valid: valid, ValidTxtPath: validPath}, closeErr
}

// countFileLines counts non-empty lines in path. A missing file returns 0, nil.
func countFileLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()
	var n int
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if len(sc.Bytes()) > 0 {
			n++
		}
	}
	return n, sc.Err()
}
