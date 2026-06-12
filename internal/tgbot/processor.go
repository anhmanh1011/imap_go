package tgbot

import (
	"bufio"
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"imap_checker/internal/checker"
	"imap_checker/internal/db"
	"imap_checker/internal/progress"
	"imap_checker/internal/proxy"
	"imap_checker/internal/result"
	"imap_checker/internal/searcher"
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

	// Progress reporter: logs CPM and counts every 30s.
	var processed atomic.Int64
	startTime := time.Now()
	stopProgress := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		var last int64
		for {
			select {
			case <-stopProgress:
				return
			case <-ticker.C:
				cur := processed.Load()
				delta := cur - last
				last = cur
				elapsed := time.Since(startTime).Seconds()
				var avgCPM int64
				if elapsed > 0 {
					avgCPM = int64(float64(cur) / elapsed * 60)
				}
				log.Printf("tgbot: progress %d/%d | CPM inst=%d avg=%d | valid=%d",
					cur, len(creds), delta*2, avgCPM,
					countFileLinesQuick(filepath.Join(outDir, "valid.txt")))
			}
		}
	}()

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for cred := range credChan {
				chk.Check(cred)
				processed.Add(1)
			}
		}()
	}
	for _, c := range creds {
		select {
		case <-ctx.Done():
			goto feedDone
		case credChan <- c:
		}
	}
feedDone:
	close(credChan)
	wg.Wait()
	close(stopProgress)

	stopFlush()
	closeErr := writer.Close()

	validPath := filepath.Join(outDir, "valid.txt")
	valid, err := countFileLines(validPath)
	if err != nil {
		return Result{}, err
	}
	return Result{Total: len(creds), Valid: valid, ValidTxtPath: validPath}, closeErr
}

// ProcessSearch runs the inbox-search pipeline for a single local input file.
// It logs into each account, searches INBOX for emails FROM searchFrom, and
// writes matching accounts to found.txt in outDir. Only accounts with ≥1
// matching email are counted as "valid" for the upload decision.
func ProcessSearch(ctx context.Context, workers int, dbPath string, pool *proxy.Pool, inputPath, outDir, searchFrom string) (Result, error) {
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

	writer, err := searcher.NewWriter(outDir)
	if err != nil {
		return Result{}, err
	}
	stopFlush := writer.StartAutoFlush()

	bar := progress.New(int64(len(creds)))
	src := searcher.New(domainMap, pool, writer, bar, searchFrom)

	credChan := make(chan checker.Credential, workers*2)
	var wg sync.WaitGroup

	var processed atomic.Int64
	startTime := time.Now()
	stopProgress := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		var last int64
		for {
			select {
			case <-stopProgress:
				return
			case <-ticker.C:
				cur := processed.Load()
				delta := cur - last
				last = cur
				elapsed := time.Since(startTime).Seconds()
				var avgCPM int64
				if elapsed > 0 {
					avgCPM = int64(float64(cur) / elapsed * 60)
				}
				log.Printf("tgbot: search progress %d/%d | CPM inst=%d avg=%d | found=%d",
					cur, len(creds), delta*2, avgCPM,
					countFileLinesQuick(filepath.Join(outDir, "found.txt")))
			}
		}
	}()

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for cred := range credChan {
				src.Search(cred)
				processed.Add(1)
			}
		}()
	}
	for _, c := range creds {
		select {
		case <-ctx.Done():
			goto feedDone
		case credChan <- c:
		}
	}
feedDone:
	close(credChan)
	wg.Wait()
	close(stopProgress)

	stopFlush()
	closeErr := writer.Close()

	foundPath := filepath.Join(outDir, "found.txt")
	found, err := countFileLines(foundPath)
	if err != nil {
		return Result{}, err
	}
	return Result{Total: len(creds), Valid: found, ValidTxtPath: foundPath}, closeErr
}

// countFileLinesQuick returns line count without error (0 on any failure). Fast path for logging.
func countFileLinesQuick(path string) int {
	n, _ := countFileLines(path)
	return n
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
