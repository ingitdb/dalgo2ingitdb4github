package dalgo2ghingitdb

import (
	"context"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/update"
)

// TestReadwriteTx_Update_SingleRecord verifies the non-batching Update path:
// read the existing record via the Contents API, apply field updates
// (set / increment / nested), write the result back, then read it again to
// confirm the persisted values. The Contents test server round-trips PUTs into
// its in-memory store, so a follow-up Get observes the written record.
func TestReadwriteTx_Update_SingleRecord(t *testing.T) {
	t.Parallel()
	// Single-record {key}.yaml → path data/tags/$records/active.yaml.
	fixtures := []githubFileFixture{{
		path:    "data/tags/$records/active.yaml",
		content: "title: Old\ncount: 5\n",
	}}
	server := newGitHubContentsServer(t, fixtures)
	defer server.Close()

	def := buildSingleRecordDef("tags", "data/tags", "{key}.yaml")
	cfg := Config{Owner: "ingitdb", Repo: "ingitdb-cli", APIBaseURL: server.URL + "/"}
	db, err := NewGitHubDBWithDef(cfg, def)
	if err != nil {
		t.Fatalf("NewGitHubDBWithDef: %v", err)
	}

	ctx := context.Background()
	err = db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Update(ctx, dal.NewKeyWithID("tags", "active"), []update.Update{
			update.ByFieldName("title", "New"),
			update.ByFieldName("count", dal.Increment(2)),
			update.ByFieldPath(update.FieldPath{"meta", "flag"}, true),
		})
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	rec := dal.NewRecordWithData(dal.NewKeyWithID("tags", "active"), map[string]any{})
	if getErr := db.Get(ctx, rec); getErr != nil {
		t.Fatalf("Get after update: %v", getErr)
	}
	data := rec.Data().(map[string]any)
	if data["title"] != "New" {
		t.Errorf("title = %v, want New", data["title"])
	}
	// YAML numbers decode as int.
	if got := data["count"]; got != int64(7) && got != 7 {
		t.Errorf("count = %v (%T), want 7", got, got)
	}
	meta, ok := data["meta"].(map[string]any)
	if !ok || meta["flag"] != true {
		t.Errorf("meta = %v, want map with flag=true", data["meta"])
	}
}

// TestReadwriteTx_Update_MissingRecord_NotFound verifies the non-batching Update
// returns not-found for a record that does not exist.
func TestReadwriteTx_Update_MissingRecord_NotFound(t *testing.T) {
	t.Parallel()
	server := newGitHubContentsServer(t, nil) // no fixtures → GET 404
	defer server.Close()

	def := buildSingleRecordDef("tags", "data/tags", "{key}.yaml")
	cfg := Config{Owner: "ingitdb", Repo: "ingitdb-cli", APIBaseURL: server.URL + "/"}
	db, err := NewGitHubDBWithDef(cfg, def)
	if err != nil {
		t.Fatalf("NewGitHubDBWithDef: %v", err)
	}

	ctx := context.Background()
	err = db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Update(ctx, dal.NewKeyWithID("tags", "missing"),
			[]update.Update{update.ByFieldName("title", "X")})
	})
	if !dal.IsNotFound(err) {
		t.Fatalf("Update missing = %v, want not-found", err)
	}
}
