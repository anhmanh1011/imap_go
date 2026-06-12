package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"imap_checker/internal/tgclient"

	"github.com/joho/godotenv"
)

func main() {
	envFile := flag.String("env", "tgbot.env", "env file with TG_API_ID, TG_API_HASH, TG_SESSION_FILE")
	outDir := flag.String("out", "./tg_download_out", "directory for downloaded files")
	channel := flag.String("channel", "valid", "channel display title to download from")
	combined := flag.String("combined", "./combined.txt", "path to write combined de-duped credentials")
	flag.Parse()

	_ = godotenv.Load(*envFile)

	apiID, err := strconv.Atoi(os.Getenv("TG_API_ID"))
	if err != nil || apiID == 0 {
		log.Fatal("TG_API_ID missing or invalid in env file")
	}
	apiHash := os.Getenv("TG_API_HASH")
	if apiHash == "" {
		log.Fatal("TG_API_HASH missing in env file")
	}
	sessionPath := os.Getenv("TG_SESSION_FILE")
	if sessionPath == "" {
		log.Fatal("TG_SESSION_FILE missing in env file")
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("create out dir: %v", err)
	}

	var downloaded []string

	opts := tgclient.Options{
		APIID:         apiID,
		APIHash:       apiHash,
		SessionPath:   sessionPath,
		InputChannel:  *channel,
		OutputChannel: *channel, // not used for upload
	}

	log.Printf("connecting to Telegram, downloading from channel %q ...", *channel)

	err = tgclient.Run(context.Background(), opts, func(ctx context.Context, c *tgclient.Client) error {
		return c.Backfill(ctx, func(fm tgclient.FileMessage) {
			safe := strings.Map(func(r rune) rune {
				if r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|' {
					return '_'
				}
				return r
			}, fm.FileName)
			dst := filepath.Join(*outDir, fmt.Sprintf("msg%d_%s", fm.MessageID, safe))

			if _, statErr := os.Stat(dst); statErr == nil {
				log.Printf("skip msg#%d %q (already exists)", fm.MessageID, fm.FileName)
				downloaded = append(downloaded, dst)
				return
			}

			start := time.Now()
			if dlErr := c.Download(ctx, fm, dst); dlErr != nil {
				log.Printf("ERROR downloading msg#%d %q: %v", fm.MessageID, fm.FileName, dlErr)
				return
			}
			info, _ := os.Stat(dst)
			size := int64(0)
			if info != nil {
				size = info.Size()
			}
			elapsed := time.Since(start)
			speed := float64(size) / elapsed.Seconds() / 1e6
			log.Printf("downloaded msg#%d %q → %.2f MB in %s (%.1f MB/s)",
				fm.MessageID, fm.FileName, float64(size)/1e6, elapsed.Round(time.Millisecond), speed)
			downloaded = append(downloaded, dst)
		})
	})
	if err != nil {
		log.Fatalf("tgclient run: %v", err)
	}

	log.Printf("downloaded %d files, combining → %s ...", len(downloaded), *combined)
	if err := combineDedup(downloaded, *combined); err != nil {
		log.Fatalf("combine: %v", err)
	}
	log.Printf("done — combined file: %s", *combined)
}

// combineDedup merges all files, deduplicates lines, writes to dst.
func combineDedup(files []string, dst string) error {
	seen := make(map[string]struct{}, 1<<20)
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	bw := bufio.NewWriterSize(out, 1<<20)
	total, unique := 0, 0

	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			log.Printf("warning: open %s: %v", f, err)
			continue
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			total++
			if _, dup := seen[line]; dup {
				continue
			}
			seen[line] = struct{}{}
			unique++
			bw.WriteString(line)
			bw.WriteByte('\n')
		}
		fh.Close()
		if err := sc.Err(); err != nil {
			log.Printf("warning: scan %s: %v", f, err)
		}
	}

	if err := bw.Flush(); err != nil {
		return err
	}
	log.Printf("combine: %d lines total → %d unique", total, unique)
	return nil
}
