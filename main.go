package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"sync"

	"imap_checker/internal/checker"
	"imap_checker/internal/db"
	"imap_checker/internal/progress"
	"imap_checker/internal/proxy"
	"imap_checker/internal/result"
)

func main() {
	inputFlag := flag.String("input", "", "credential file (user:pass per line) [required]")
	workersFlag := flag.Int("workers", 0, "number of concurrent goroutines [required]")
	proxiesFlag := flag.String("proxies", "", "proxy file (ip:port per line) [optional]")
	dbFlag := flag.String("db", "./Servers.db", "path to Servers.db")
	outFlag := flag.String("out", "./output", "output directory for result files")
	flag.Parse()

	if *inputFlag == "" {
		fmt.Fprintln(os.Stderr, "error: -input is required")
		flag.Usage()
		os.Exit(1)
	}
	if *workersFlag <= 0 {
		fmt.Fprintln(os.Stderr, "error: -workers must be a positive integer")
		flag.Usage()
		os.Exit(1)
	}

	// ── Phase 1: Setup ────────────────────────────────────────────────────────

	log.Printf("reading credentials from %s ...", *inputFlag)
	creds, err := checker.ParseFile(*inputFlag)
	if err != nil {
		log.Fatalf("parse credentials: %v", err)
	}
	log.Printf("loaded %d credentials", len(creds))

	domains := checker.UniqueDomains(creds)
	log.Printf("resolving %d unique domains from %s ...", len(domains), *dbFlag)
	domainMap, err := db.BatchLookup(*dbFlag, domains)
	if err != nil {
		log.Fatalf("db lookup: %v", err)
	}
	found, fallback := countMap(domainMap)
	log.Printf("domain resolution complete: %d in DB, %d fallback (imap.<domain>:993)", found, fallback)

	pool, err := proxy.LoadFile(*proxiesFlag)
	if err != nil {
		log.Fatalf("load proxies: %v", err)
	}
	if pool.Len() > 0 {
		log.Printf("loaded %d proxies", pool.Len())
	} else {
		log.Printf("no proxy file — using direct dial")
	}

	writer, err := result.New(*outFlag)
	if err != nil {
		log.Fatalf("create output dir: %v", err)
	}
	stopFlush := writer.StartAutoFlush()

	// ── Phase 2: Concurrent check ─────────────────────────────────────────────

	bar := progress.New(int64(len(creds)))
	chk := checker.New(domainMap, pool, writer, bar)
	stopBar := bar.Start()

	credChan := make(chan checker.Credential, *workersFlag*2)

	var wg sync.WaitGroup
	for i := 0; i < *workersFlag; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for cred := range credChan {
				chk.Check(cred)
			}
		}()
	}

	for _, c := range creds {
		credChan <- c
	}
	close(credChan)

	wg.Wait()

	// ── Shutdown ──────────────────────────────────────────────────────────────

	stopFlush()
	stopBar()
	if err := writer.Close(); err != nil {
		log.Printf("warning: output writer reported errors (results may be incomplete): %v", err)
	}

	fmt.Printf("\nResults saved to %s/\n", *outFlag)
}

func countMap(m map[string]db.ServerInfo) (found, fallback int) {
	for _, v := range m {
		if v.Fallback {
			fallback++
		} else {
			found++
		}
	}
	return
}
