package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/discrawl/internal/adapter"
	"github.com/openclaw/discrawl/internal/config"
	"github.com/openclaw/discrawl/internal/store"
)

func TestBuildDiscordMessageRecord(t *testing.T) {
	created := time.Date(2026, 6, 27, 12, 30, 0, 0, time.UTC)
	msg := store.MessageRow{
		MessageID:      "111",
		GuildID:        "g1",
		ChannelID:      "c1",
		ChannelName:    "general",
		AuthorID:       "u1",
		AuthorName:     "Alice",
		Content:        "hello world",
		CreatedAt:      created,
		ReplyToMessage: "110",
		HasAttachments: true,
		Pinned:         true,
	}
	ch := store.ChannelRow{ID: "c1", GuildID: "g1", Kind: "text", Name: "general", Topic: "chitchat"}

	rec := buildDiscordMessageRecord(msg, ch, "/tmp/discrawl.db", "9.9.9")

	if rec.Schema != adapter.SchemaV1 {
		t.Fatalf("schema = %q, want %q", rec.Schema, adapter.SchemaV1)
	}
	if rec.Source.Kind != "discord" || rec.Source.Version != "9.9.9" {
		t.Fatalf("source = %+v", rec.Source)
	}
	if rec.Collection.ExternalID != "discord:channel:c1" || rec.Collection.Kind != "discord_channel" || rec.Collection.Name != "general" {
		t.Fatalf("collection = %+v", rec.Collection)
	}
	if rec.Item.ExternalID != "discord:message:111" || rec.Item.Kind != "message" || rec.Item.Text != "hello world" {
		t.Fatalf("item = %+v", rec.Item)
	}
	if rec.Item.CreatedAt != created.Format(time.RFC3339Nano) {
		t.Fatalf("created_at = %q", rec.Item.CreatedAt)
	}
	if rec.Actor == nil || rec.Actor.ExternalID != "discord:user:u1" || rec.Actor.Type != "human" {
		t.Fatalf("actor = %+v", rec.Actor)
	}
	if len(rec.Relations) != 1 || rec.Relations[0].TargetExternalID != "discord:message:110" || rec.Relations[0].Type != "reply_to" {
		t.Fatalf("relations = %+v", rec.Relations)
	}

	// The emitted record must round-trip through the contract validator that
	// MiseLedger uses on ingest.
	line, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := adapter.Parse(line); err != nil {
		t.Fatalf("adapter.Parse rejected emitted record: %v", err)
	}
}

func TestBuildDiscordMessageRecordSystemActorFallback(t *testing.T) {
	// A message with no author id (rare system message) must still validate.
	rec := buildDiscordMessageRecord(store.MessageRow{
		MessageID: "200",
		ChannelID: "c2",
		Content:   "system notice",
	}, store.ChannelRow{}, "", "")

	if rec.Actor.Type != "system" {
		t.Fatalf("actor type = %q, want system", rec.Actor.Type)
	}
	if rec.Collection.Name != "c2" {
		t.Fatalf("collection name fallback = %q, want c2", rec.Collection.Name)
	}
	if len(rec.Relations) != 0 {
		t.Fatalf("expected no relations, got %+v", rec.Relations)
	}
	line, _ := json.Marshal(rec)
	if _, err := adapter.Parse(line); err != nil {
		t.Fatalf("adapter.Parse rejected fallback record: %v", err)
	}
}

func TestRunExportAdapterFromSyntheticArchive(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	dbPath := filepath.Join(dir, "discrawl.db")
	cfg := config.Default()
	cfg.DBPath = dbPath
	cfg.Discord.TokenSource = "none"
	if err := config.Write(cfgPath, cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}

	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := s.UpsertGuild(ctx, store.GuildRecord{ID: "g1", Name: "Guild One", RawJSON: `{}`}); err != nil {
		t.Fatalf("upsert guild: %v", err)
	}
	if err := s.UpsertChannel(ctx, store.ChannelRecord{ID: "c1", GuildID: "g1", Kind: "text", Name: "general", Topic: "shipping", RawJSON: `{}`}); err != nil {
		t.Fatalf("upsert channel: %v", err)
	}
	if err := s.ReplaceMembers(ctx, "g1", []store.MemberRecord{
		{GuildID: "g1", UserID: "u1", Username: "alice", DisplayName: "Alice", RawJSON: `{}`},
		{GuildID: "g1", UserID: "u2", Username: "bob", DisplayName: "Bob", RawJSON: `{}`},
	}); err != nil {
		t.Fatalf("replace members: %v", err)
	}
	base := time.Date(2026, 6, 27, 12, 30, 0, 0, time.UTC)
	messages := []store.MessageRecord{
		{ID: "m1", GuildID: "g1", ChannelID: "c1", ChannelName: "general", AuthorID: "u1", AuthorName: "Alice", CreatedAt: base.Format(time.RFC3339Nano), Content: "first note", NormalizedContent: "first note", RawJSON: `{"author":{"username":"alice","global_name":"Alice"}}`},
		{ID: "m2", GuildID: "g1", ChannelID: "c1", ChannelName: "general", AuthorID: "u2", AuthorName: "Bob", CreatedAt: base.Add(time.Minute).Format(time.RFC3339Nano), Content: "second note", NormalizedContent: "second note", ReplyToMessageID: "m1", RawJSON: `{"author":{"username":"bob","global_name":"Bob"}}`},
		{ID: "m3", GuildID: "g1", ChannelID: "c1", ChannelName: "general", AuthorID: "u1", AuthorName: "Alice", CreatedAt: base.Add(2 * time.Minute).Format(time.RFC3339Nano), Content: "third note", NormalizedContent: "third note", HasAttachments: true, Pinned: true, RawJSON: `{"author":{"username":"alice","global_name":"Alice"}}`},
	}
	for _, msg := range messages {
		if err := s.UpsertMessage(ctx, msg); err != nil {
			t.Fatalf("upsert message %s: %v", msg.ID, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := Run(ctx, []string{"--config", cfgPath, "export", "adapter", "--out", "-"}, &stdout, &stderr); err != nil {
		t.Fatalf("run export adapter: %v", err)
	}
	if got := stderr.String(); !strings.Contains(got, "exported 3 discord message(s) to miseledger.adapter.v1") {
		t.Fatalf("stderr = %q", got)
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("line count = %d, want 3\n%s", len(lines), stdout.String())
	}
	var records []adapter.Record
	for _, line := range lines {
		rec, err := adapter.Parse([]byte(line))
		if err != nil {
			t.Fatalf("parse exported line %q: %v", line, err)
		}
		records = append(records, rec)
	}
	if records[0].Item.ExternalID != "discord:message:m1" || records[0].Actor.Name != "Alice" {
		t.Fatalf("first record = %+v", records[0])
	}
	if len(records[1].Relations) != 1 || records[1].Relations[0].TargetExternalID != "discord:message:m1" {
		t.Fatalf("reply relation = %+v", records[1].Relations)
	}
	if records[2].Raw.Path != dbPath {
		t.Fatalf("raw path = %q, want %q", records[2].Raw.Path, dbPath)
	}
	var metadata map[string]any
	if err := json.Unmarshal(records[2].Item.Metadata, &metadata); err != nil {
		t.Fatalf("metadata: %v", err)
	}
	if metadata["has_attachments"] != true || metadata["pinned"] != true {
		t.Fatalf("metadata = %+v", metadata)
	}
}
