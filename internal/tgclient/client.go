package tgclient

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
)

// FileMessage describes a .txt document found in the input channel.
type FileMessage struct {
	MessageID int64
	ChannelID int64
	FileName  string
	doc       *tg.Document
}

// Client is a connected userbot. All methods must be called inside Run's callback.
type Client struct {
	api        *tg.Client
	sender     *message.Sender
	inputPeer  tg.InputPeerClass
	outputPeer tg.InputPeerClass
	inputChID  int64
}

// Options configures Run.
type Options struct {
	APIID         int
	APIHash       string
	SessionPath   string
	InputChannel  string // @username
	OutputChannel string // @username
	OnFile        func(FileMessage) // invoked for realtime .txt messages
}

// Run connects using the pre-authorized session file and invokes fn with a
// ready Client. The realtime dispatcher (Options.OnFile) is active before fn is called.
func Run(ctx context.Context, opts Options, fn func(ctx context.Context, c *Client) error) error {
	storage := &session.FileStorage{Path: opts.SessionPath}
	dispatcher := tg.NewUpdateDispatcher()

	client := telegram.NewClient(opts.APIID, opts.APIHash, telegram.Options{
		SessionStorage: storage,
		UpdateHandler:  dispatcher,
	})

	return client.Run(ctx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return fmt.Errorf("auth status: %w", err)
		}
		if !status.Authorized {
			return errors.New("session not authorized (provide a valid gotd session file)")
		}

		api := client.API()
		inPeer, inCh, err := resolveChannel(ctx, api, opts.InputChannel)
		if err != nil {
			return fmt.Errorf("resolve input channel: %w", err)
		}
		outPeer, _, err := resolveChannel(ctx, api, opts.OutputChannel)
		if err != nil {
			return fmt.Errorf("resolve output channel: %w", err)
		}

		c := &Client{
			api:        api,
			sender:     message.NewSender(api),
			inputPeer:  inPeer,
			outputPeer: outPeer,
			inputChID:  inCh,
		}

		if opts.OnFile != nil {
			dispatcher.OnNewChannelMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewChannelMessage) error {
				if fm, ok := c.fileMessageFrom(u.Message); ok {
					opts.OnFile(fm)
				}
				return nil
			})
		}

		return fn(ctx, c)
	})
}

func resolveChannel(ctx context.Context, api *tg.Client, username string) (tg.InputPeerClass, int64, error) {
	name := strings.TrimPrefix(username, "@")
	res, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: name})
	if err != nil {
		return nil, 0, err
	}
	for _, ch := range res.Chats {
		if channel, ok := ch.(*tg.Channel); ok {
			return &tg.InputPeerChannel{
				ChannelID:  channel.ID,
				AccessHash: channel.AccessHash,
			}, channel.ID, nil
		}
	}
	return nil, 0, fmt.Errorf("no channel found for %q", username)
}

func (c *Client) fileMessageFrom(msg tg.MessageClass) (FileMessage, bool) {
	m, ok := msg.(*tg.Message)
	if !ok {
		return FileMessage{}, false
	}
	media, ok := m.Media.(*tg.MessageMediaDocument)
	if !ok {
		return FileMessage{}, false
	}
	doc, ok := media.Document.(*tg.Document)
	if !ok {
		return FileMessage{}, false
	}
	var filename string
	for _, attr := range doc.Attributes {
		if fn, ok := attr.(*tg.DocumentAttributeFilename); ok {
			filename = fn.FileName
		}
	}
	if !strings.HasSuffix(strings.ToLower(filename), ".txt") {
		return FileMessage{}, false
	}
	return FileMessage{
		MessageID: int64(m.ID),
		ChannelID: c.inputChID,
		FileName:  filename,
		doc:       doc,
	}, true
}

// Backfill walks history of the input channel from newest to oldest, paginating
// backwards, and calls onFile for every .txt document found.
func (c *Client) Backfill(ctx context.Context, onFile func(FileMessage)) error {
	offsetID := 0
	for {
		hist, err := c.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:     c.inputPeer,
			OffsetID: offsetID,
			Limit:    100,
		})
		if err != nil {
			return fmt.Errorf("get history: %w", err)
		}
		ch, ok := hist.(*tg.MessagesChannelMessages)
		if !ok {
			return fmt.Errorf("unexpected history type %T", hist)
		}
		if len(ch.Messages) == 0 {
			return nil
		}
		minID := offsetID
		for _, mc := range ch.Messages {
			if m, ok := mc.(*tg.Message); ok {
				if minID == 0 || m.ID < minID {
					minID = m.ID
				}
				if fm, ok := c.fileMessageFrom(m); ok {
					onFile(fm)
				}
			}
		}
		if minID == offsetID || minID == 0 {
			return nil
		}
		offsetID = minID
	}
}

// Download saves the document attached to fm into the file at dst.
func (c *Client) Download(ctx context.Context, fm FileMessage, dst string) error {
	loc := &tg.InputDocumentFileLocation{
		ID:            fm.doc.ID,
		AccessHash:    fm.doc.AccessHash,
		FileReference: fm.doc.FileReference,
		ThumbSize:     "",
	}
	_, err := downloader.NewDownloader().Download(c.api, loc).ToPath(ctx, dst)
	return err
}

// UploadFile uploads the file at path to the output channel with caption.
func (c *Client) UploadFile(ctx context.Context, path, caption string) error {
	up := uploader.NewUploader(c.api)
	f, err := up.FromPath(ctx, path)
	if err != nil {
		return fmt.Errorf("upload from path: %w", err)
	}
	doc := message.UploadedDocument(f, styling.Plain(caption)).Filename("valid.txt")
	_, err = c.sender.To(c.outputPeer).Media(ctx, doc)
	return err
}

// SendText sends a plain-text message to the output channel.
func (c *Client) SendText(ctx context.Context, text string) error {
	_, err := c.sender.To(c.outputPeer).Text(ctx, text)
	return err
}
