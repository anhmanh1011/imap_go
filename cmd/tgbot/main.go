package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"imap_checker/internal/proxy"
	"imap_checker/internal/tgbot"
	"imap_checker/internal/tgclient"
)

func main() {
	envPath := "tgbot.env"
	if len(os.Args) > 1 {
		envPath = os.Args[1]
	}

	cfg, err := tgbot.Load(envPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	pool, stopPool := buildPool(cfg)
	defer stopPool()

	state, err := tgbot.NewState(cfg.StateDB)
	if err != nil {
		log.Fatalf("state: %v", err)
	}
	defer state.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	process := func(ctx context.Context, inputPath, outDir string) (tgbot.Result, error) {
		return tgbot.Process(ctx, cfg.Workers, cfg.DBPath, pool, inputPath, outDir)
	}

	// enqueueRT is set inside the callback once bot is ready; the OnFile closure
	// captures it by pointer so realtime events enqueue correctly even though
	// the dispatcher is registered before bot is constructed.
	var enqueueRT func(tgclient.FileMessage)

	tgErr := tgclient.Run(ctx, tgclient.Options{
		APIID:         cfg.APIID,
		APIHash:       cfg.APIHash,
		SessionPath:   cfg.SessionPath,
		InputChannel:  cfg.InputChannel,
		OutputChannel: cfg.OutputChannel,
		OnFile: func(fm tgclient.FileMessage) {
			if enqueueRT != nil {
				enqueueRT(fm)
			}
		},
	}, func(ctx context.Context, c *tgclient.Client) error {
		out := &clientOutput{c: c}
		bot := tgbot.NewBot(state, out, process, cfg.WorkDir)
		go bot.Run(ctx)

		enqueue := func(fm tgclient.FileMessage) {
			bot.Jobs() <- toJob(c, fm)
		}
		enqueueRT = enqueue

		// Clear incomplete rows so backfill re-processes them.
		if n, err := state.DeleteIncomplete(); err != nil {
			log.Printf("tgbot: clear incomplete rows: %v", err)
		} else if n > 0 {
			log.Printf("tgbot: cleared %d incomplete rows for retry", n)
		}

		log.Printf("tgbot: starting backfill ...")
		if err := c.Backfill(ctx, enqueue); err != nil {
			log.Printf("tgbot: backfill error: %v", err)
		}
		log.Printf("tgbot: backfill complete, entering realtime")

		<-ctx.Done()
		return ctx.Err()
	})

	if tgErr != nil && tgErr != context.Canceled {
		log.Fatalf("tgbot run: %v", tgErr)
	}
	log.Printf("tgbot: shutdown")
}

func toJob(c *tgclient.Client, fm tgclient.FileMessage) tgbot.Job {
	return tgbot.Job{
		MessageID: fm.MessageID,
		ChannelID: fm.ChannelID,
		FileName:  fm.FileName,
		Download: func(ctx context.Context, dst string) error {
			return c.Download(ctx, fm, dst)
		},
	}
}

type clientOutput struct{ c *tgclient.Client }

func (o *clientOutput) UploadFile(ctx context.Context, path, caption string) error {
	return o.c.UploadFile(ctx, path, caption)
}
func (o *clientOutput) SendText(ctx context.Context, text string) error {
	return o.c.SendText(ctx, text)
}

func buildPool(cfg tgbot.Config) (*proxy.Pool, func()) {
	if cfg.ProxyURL != "" {
		p := proxy.New(proxy.KindSOCKS5)
		stop, err := proxy.StartURLPoller(p, cfg.ProxyURL, cfg.ProxyRefresh, log.Default())
		if err != nil {
			log.Fatalf("proxy url poller: %v", err)
		}
		log.Printf("tgbot: %d SOCKS5 proxies (refresh %s)", p.Len(), cfg.ProxyRefresh)
		return p, stop
	}
	p, err := proxy.LoadFile(cfg.ProxiesFile)
	if err != nil {
		log.Fatalf("load proxies: %v", err)
	}
	log.Printf("tgbot: %d HTTP-CONNECT proxies", p.Len())
	return p, func() {}
}
