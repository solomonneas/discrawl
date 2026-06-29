package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/steipete/discrawl/internal/adapter"
	"github.com/steipete/discrawl/internal/store"
)

// runExport routes `discrawl export <subcommand>`. Today the only target is the
// miseledger.adapter.v1 JSONL contract consumed by MiseLedger.
func (r *runtime) runExport(args []string) error {
	if len(args) == 0 {
		return usageErr(fmt.Errorf("export requires a subcommand (adapter)"))
	}
	switch args[0] {
	case "adapter":
		return r.runExportAdapter(args[1:])
	default:
		return usageErr(fmt.Errorf("unknown export subcommand %q", args[0]))
	}
}

// runExportAdapter walks the local archive and emits one miseledger.adapter.v1
// JSON record per Discord message to stdout (or --out), so the common pipe is:
//
//	discrawl export adapter | miseledger crawl adapter -
//
// The progress summary goes to stderr to keep stdout a clean JSONL stream.
func (r *runtime) runExportAdapter(args []string) error {
	fs := flag.NewFlagSet("export adapter", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sinceFlag := fs.String("since", "", "only messages at or after this RFC3339 time")
	limit := fs.Int("limit", 0, "maximum messages to emit (0 = all)")
	channel := fs.String("channel", "", "restrict to a channel id or name")
	guildFlag := fs.String("guild", "", "restrict to a guild id")
	guildsFlag := fs.String("guilds", "", "restrict to comma-separated guild ids")
	outPath := fs.String("out", "-", "output file or - for stdout")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() != 0 {
		return usageErr(fmt.Errorf("export adapter takes no positional arguments"))
	}

	var since time.Time
	if trimmed := strings.TrimSpace(*sinceFlag); trimmed != "" {
		parsed, err := time.Parse(time.RFC3339, trimmed)
		if err != nil {
			return usageErr(fmt.Errorf("invalid --since %q: %w", *sinceFlag, err))
		}
		since = parsed
	}

	// Channel rows give each message's collection a real name, kind, and topic.
	// An empty guild id returns channels across every archived guild.
	channels, err := r.store.Channels(r.ctx, "")
	if err != nil {
		return err
	}
	channelByID := make(map[string]store.ChannelRow, len(channels))
	for _, c := range channels {
		channelByID[c.ID] = c
	}

	messages, err := r.store.ListMessages(r.ctx, store.MessageListOptions{
		GuildIDs: r.resolveSearchGuilds(*guildFlag, *guildsFlag),
		Channel:  *channel,
		Since:    since,
		Limit:    *limit,
	})
	if err != nil {
		return err
	}

	w := r.stdout
	if *outPath != "-" {
		f, err := os.Create(*outPath)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		w = f
	}

	enc := json.NewEncoder(w)
	count := 0
	for _, m := range messages {
		rec := buildDiscordMessageRecord(m, channelByID[m.ChannelID], r.cfg.DBPath, version)
		if err := enc.Encode(rec); err != nil {
			return err
		}
		count++
	}
	fmt.Fprintf(r.stderr, "exported %d discord message(s) to miseledger.adapter.v1\n", count)
	return nil
}

// buildDiscordMessageRecord maps one archived Discord message (plus its channel)
// onto the adapter contract: the channel is the collection, the message is the
// item, the author is the actor, and a reply becomes a reply_to relation. It is
// a pure function so the mapping can be unit tested without a live store.
func buildDiscordMessageRecord(m store.MessageRow, ch store.ChannelRow, dbPath, sourceVersion string) adapter.Record {
	createdAt := ""
	if !m.CreatedAt.IsZero() {
		createdAt = m.CreatedAt.UTC().Format(time.RFC3339Nano)
	}

	channelName := firstNonEmpty(m.ChannelName, ch.Name, m.ChannelID)

	collectionMeta := map[string]any{"guild_id": m.GuildID}
	if ch.Kind != "" {
		collectionMeta["channel_kind"] = ch.Kind
	}
	if ch.Topic != "" {
		collectionMeta["topic"] = ch.Topic
	}
	if ch.ParentID != "" {
		collectionMeta["parent_id"] = ch.ParentID
	}

	itemMeta := map[string]any{
		"guild_id":        m.GuildID,
		"channel_id":      m.ChannelID,
		"pinned":          m.Pinned,
		"has_attachments": m.HasAttachments,
	}

	// An author id is a stable Discord snowflake; fall back to the display name
	// only when the archive lacks an id (rare/system messages).
	actorName := firstNonEmpty(m.AuthorName, m.AuthorID)
	actorType := "human"
	actorKey := m.AuthorID
	if actorKey == "" {
		actorKey = actorName
		actorType = "system"
	}

	relations := []adapter.Relation{}
	if m.ReplyToMessage != "" {
		relations = append(relations, adapter.Relation{
			TargetExternalID: "discord:message:" + m.ReplyToMessage,
			Type:             "reply_to",
		})
	}

	externalID := "discord:message:" + m.MessageID
	rawSeed := []byte(m.MessageID + "\x1f" + m.Content + "\x1f" + createdAt)

	return adapter.Record{
		Schema: adapter.SchemaV1,
		Source: adapter.Source{Kind: "discord", Name: "discord", Version: sourceVersion},
		Collection: adapter.Collection{
			ExternalID: "discord:channel:" + m.ChannelID,
			Kind:       "discord_channel",
			Name:       channelName,
			Metadata:   metadataJSON(collectionMeta),
		},
		Item: adapter.Item{
			ExternalID: externalID,
			Kind:       "message",
			CreatedAt:  createdAt,
			Text:       m.Content,
			Tags:       []string{"discord", "message"},
			Metadata:   metadataJSON(itemMeta),
		},
		Actor: &adapter.Actor{
			ExternalID: "discord:user:" + actorKey,
			Type:       actorType,
			Name:       actorName,
		},
		Artifacts: []adapter.Artifact{},
		Links:     []adapter.Link{},
		Relations: relations,
		Raw: adapter.RawRef{
			Format: "discord/message",
			Hash:   "sha256:" + hashHex(rawSeed),
			Path:   dbPath,
		},
	}
}

func metadataJSON(v map[string]any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
