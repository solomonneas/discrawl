package cli

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/steipete/discrawl/internal/adapter"
	"github.com/steipete/discrawl/internal/store"
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
