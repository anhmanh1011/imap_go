// remap-valid reads a valid.txt (user:pass per line) and writes mail:pass:server:port
// using Servers.db for the IMAP host/port lookup.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"imap_checker/internal/db"
)

func main() {
	input := flag.String("in", "", "path to valid.txt (user:pass lines)")
	output := flag.String("out", "", "output file path (default: stdout)")
	dbPath := flag.String("db", "Servers.db", "path to Servers.db")
	flag.Parse()

	if *input == "" {
		log.Fatal("-in is required")
	}

	f, err := os.Open(*input)
	if err != nil {
		log.Fatalf("open input: %v", err)
	}
	defer f.Close()

	// First pass: collect all unique domains.
	type cred struct{ user, pass string }
	var creds []cred
	domains := map[string]struct{}{}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r\n")
		if line == "" {
			continue
		}
		// Split on first ':' only for user, rest is pass.
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		user := line[:idx]
		pass := line[idx+1:]
		creds = append(creds, cred{user, pass})

		at := strings.LastIndex(user, "@")
		if at >= 0 {
			domains[strings.ToLower(user[at+1:])] = struct{}{}
		}
	}
	if err := scanner.Err(); err != nil {
		log.Fatalf("read input: %v", err)
	}

	domainList := make([]string, 0, len(domains))
	for d := range domains {
		domainList = append(domainList, d)
	}

	serverMap, err := db.BatchLookup(*dbPath, domainList)
	if err != nil {
		log.Fatalf("db lookup: %v", err)
	}

	var out *os.File
	if *output == "" {
		out = os.Stdout
	} else {
		out, err = os.Create(*output)
		if err != nil {
			log.Fatalf("create output: %v", err)
		}
		defer out.Close()
	}

	w := bufio.NewWriter(out)
	for _, c := range creds {
		at := strings.LastIndex(c.user, "@")
		var server string
		var port int
		if at >= 0 {
			domain := strings.ToLower(c.user[at+1:])
			if info, ok := serverMap[domain]; ok {
				server = info.Host
				port = info.Port
			}
		}
		if server == "" {
			server = "imap." + strings.ToLower(c.user[at+1:])
			port = 993
		}
		fmt.Fprintf(w, "%s:%s:%s:%d\n", c.user, c.pass, server, port)
	}
	w.Flush()
}
