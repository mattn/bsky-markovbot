package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	comatproto "github.com/bluesky-social/indigo/api/atproto"
	"github.com/bluesky-social/indigo/api/bsky"
	cliutil "github.com/bluesky-social/indigo/cmd/gosky/util"
	lexutil "github.com/bluesky-social/indigo/lex/util"
	"github.com/bluesky-social/indigo/xrpc"
	"github.com/ikawaha/kagome-dict/uni"
	"github.com/ikawaha/kagome/v2/tokenizer"
	markov "github.com/mattn/go-markov"
)

const name = "bsky-markovbot"

const version = "0.0.8"

var revision = "HEAD"

type Bot struct {
	Host     string
	Handle   string
	Password string
	xrpcc    *xrpc.Client
}

func (bot *Bot) makeXRPCC() (*xrpc.Client, error) {
	xrpcc := &xrpc.Client{
		Client: cliutil.NewHttpClient(),
		Host:   bot.Host,
		Auth:   &xrpc.AuthInfo{Handle: bot.Handle},
	}
	auth, err := comatproto.ServerCreateSession(context.TODO(), xrpcc, &comatproto.ServerCreateSession_Input{
		Identifier: xrpcc.Auth.Handle,
		Password:   bot.Password,
	})
	if err != nil {
		return nil, fmt.Errorf("cannot create session: %w", err)
	}
	xrpcc.Auth.Did = auth.Did
	xrpcc.Auth.AccessJwt = auth.AccessJwt
	xrpcc.Auth.RefreshJwt = auth.RefreshJwt
	return xrpcc, nil
}

func getenv(name, def string) string {
	s := os.Getenv(name)
	if s == "" {
		return def
	}
	return s
}

func contains(a []string, s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
}

func run(dryrun bool, word string) error {
	length := -1

	var bot Bot
	bot.Host = getenv("MARKOVBOT_HOST", "https://bsky.social")
	bot.Handle = getenv("MARKOVBOT_HANDLE", "markovbot.bsky.social")
	bot.Password = os.Getenv("MARKOVBOT_PASSWORD")

	if bot.Password == "" {
		log.Fatal("MARKOVBOT_PASSWORD is required")
	}

	var err error
	bot.xrpcc, err = bot.makeXRPCC()
	if err != nil {
		return fmt.Errorf("cannot create client: %w", err)
	}

	var feed []*bsky.FeedDefs_FeedViewPost
	var cursor string
	for {
		resp, err := bsky.FeedGetTimeline(context.TODO(), bot.xrpcc, "reverse-chronological", cursor, 20)
		if err != nil {
			log.Fatal(err)
		}
		feed = append(feed, resp.Feed...)
		if resp.Cursor != nil {
			cursor = *resp.Cursor
		} else {
			cursor = ""
		}
		if cursor == "" || int64(len(feed)) > 20 {
			break
		}
	}

	m := markov.New()
	for _, p := range feed {
		rec := p.Post.Record.Val.(*bsky.FeedPost)
		m.Update(strings.TrimSpace(rec.Text))
	}

	t, err := tokenizer.New(uni.Dict(), tokenizer.OmitBosEos())
	if err != nil {
		log.Fatal(err)
	}

	bad := []string{
		"助詞",
		"補助記号",
	}
	var result string
	var limit int
	for {
		if limit++; limit > 500 {
			return errors.New("retry max")
		}
		var first string
		if word != "" {
			first = word
			word = ""
		} else {
			for {
				if limit++; limit > 500 {
					return errors.New("retry max")
				}
				first = m.First()
				tokens := t.Tokenize(first)
				if !contains(bad, tokens[0].Features()[0]) {
					break
				}
			}
		}

		result = strings.TrimSpace(m.Chain(first))
		if result != "" && (length == -1 || len([]rune(result)) <= length) {
			break
		}
	}

	if dryrun {
		fmt.Println(result)
		return nil
	}

	post := &bsky.FeedPost{
		LexiconTypeID: "app.bsky.feed.post",
		Text:          result,
		CreatedAt:     time.Now().Local().Format(time.RFC3339),
	}

	var lastErr error
	for retry := 0; retry < 3; retry++ {
		resp, err := comatproto.RepoCreateRecord(context.TODO(), bot.xrpcc, &comatproto.RepoCreateRecord_Input{
			Collection: "app.bsky.feed.post",
			Repo:       bot.xrpcc.Auth.Did,
			Record: &lexutil.LexiconTypeDecoder{
				Val: post,
			},
		})
		if err == nil {
			log.Println(result)
			log.Println(resp.Uri)
			return nil
		}
		log.Printf("failed to create post: %v", err)
		lastErr = err
		time.Sleep(time.Second)
	}

	return fmt.Errorf("failed to create post: %w", lastErr)
}

func main() {
	var ver bool
	var dryrun bool
	flag.BoolVar(&ver, "v", false, "show version")
	flag.BoolVar(&dryrun, "dryrun", false, "dryrun")
	flag.Parse()

	if ver {
		fmt.Println(version)
		os.Exit(0)
	}

	if err := run(dryrun, flag.Arg(0)); err != nil {
		log.Fatal(err)
	}
}
