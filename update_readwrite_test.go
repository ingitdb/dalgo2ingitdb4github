package dalgo2ghingitdb

import (
	"context"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/dal-go/record/update"
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
		return tx.Update(ctx, record.NewKeyWithID("tags", "active"), []update.Update{
			update.ByFieldName("title", "New"),
			update.ByFieldName("count", dal.Increment(2)),
			update.ByFieldPath(update.FieldPath{"meta", "flag"}, true),
		})
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	rec := record.NewRecordWithData(record.NewKeyWithID("tags", "active"), map[string]any{})
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
		return tx.Update(ctx, record.NewKeyWithID("tags", "missing"),
			[]update.Update{update.ByFieldName("title", "X")})
	})
	if !record.IsNotFound(err) {
		t.Fatalf("Update missing = %v, want not-found", err)
	}
}

type stubTransform struct {
	name string
	val  any
}

func (s stubTransform) Name() string { return s.name }
func (s stubTransform) Value() any   { return s.val }

func TestReadwriteTx_Update_And_ReadonlyTx_Errors(t *testing.T) {
	t.Parallel()
	fixtures := []githubFileFixture{{
		path:    "data/tags/$records/active.yaml",
		content: "title: Old\n",
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
	key := record.NewKeyWithID("tags", "active")

	// Preconditions not supported.
	err = db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Update(ctx, key, []update.Update{update.ByFieldName("title", "New")}, dal.WithExistsPrecondition())
	})
	if err == nil {
		t.Fatal("expected error for Update with preconditions")
	}

	// Get error inside Update (collection with nil RecordFile -> resolveCollection returns non-not-found error).
	noRecFileDef := buildSingleRecordDef("tags", "data/tags", "{key}.yaml")
	noRecFileDef.Collections["tags"].RecordFile = nil
	noRecFileDB, err := NewGitHubDBWithDef(cfg, noRecFileDef)
	if err != nil {
		t.Fatalf("NewGitHubDBWithDef: %v", err)
	}
	err = noRecFileDB.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Update(ctx, key, []update.Update{update.ByFieldName("title", "New")})
	})
	if err == nil {
		t.Fatal("expected error for Update when collection has nil RecordFile")
	}

	// applyUpdates error inside Update.
	err = db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Update(ctx, key, []update.Update{update.ByFieldName("title", dal.Increment(1))})
	})
	if err == nil {
		t.Fatal("expected error for Update with invalid increment target")
	}

	// readonlyTx.resolveCollection(nil), Exists, and GetMulti error paths.
	rtx := readonlyTx{db: &githubDB{def: noRecFileDef}}
	if _, _, err := rtx.resolveCollection(nil); err == nil {
		t.Fatal("expected resolveCollection(nil) error")
	}
	if _, err := rtx.Exists(ctx, key); err == nil {
		t.Fatal("expected Exists error when collection has nil RecordFile")
	}
	nilDefTx := readonlyTx{db: &githubDB{def: nil}}
	if err := nilDefTx.GetMulti(ctx, []record.Record{record.NewRecordWithData(key, map[string]any{})}); err == nil {
		t.Fatal("expected GetMulti error when def is nil")
	}
}

type emptyUpdate struct{}

func (emptyUpdate) FieldName() string           { return "" }
func (emptyUpdate) FieldPath() update.FieldPath { return nil }
func (emptyUpdate) Value() any                  { return nil }

func TestApplyUpdates_AllBranches(t *testing.T) {
	t.Parallel()

	// 1. Empty FieldName and empty FieldPath -> skipped silently.
	data := map[string]any{"a": 1}
	if err := applyUpdates(data, []update.Update{emptyUpdate{}}); err != nil {
		t.Fatalf("empty path update: %v", err)
	}

	// 2. Error via FieldPath in applyUpdates (non-map intermediate).
	data = map[string]any{"a": "not-a-map"}
	if err := applyUpdates(data, []update.Update{update.ByFieldPath(update.FieldPath{"a", "b"}, "v")}); err == nil {
		t.Fatal("expected error when setting nested path through non-map")
	}

	// 3. Existing map intermediate in applyFieldUpdate.
	data = map[string]any{"a": map[string]any{"x": 1}}
	if err := applyUpdates(data, []update.Update{update.ByFieldPath(update.FieldPath{"a", "y"}, 2)}); err != nil {
		t.Fatalf("nested set into existing map: %v", err)
	}
	if data["a"].(map[string]any)["y"] != 2 {
		t.Fatalf("expected a.y=2, got %v", data["a"])
	}

	// 4. Unsupported transform in applyFieldUpdate.
	if err := applyUpdates(data, []update.Update{update.ByFieldName("a", stubTransform{name: "custom_transform", val: 1})}); err == nil {
		t.Fatal("expected error for unsupported transform")
	}

	// 5. applyDelete nested paths: missing intermediate, non-map intermediate, and existing map intermediate.
	data = map[string]any{
		"scalar": "str",
		"nested": map[string]any{"drop": "me", "keep": "ok"},
	}
	if err := applyUpdates(data, []update.Update{
		update.ByFieldPath(update.FieldPath{"missing", "leaf"}, update.DeleteField),
		update.ByFieldPath(update.FieldPath{"scalar", "leaf"}, update.DeleteField),
		update.ByFieldPath(update.FieldPath{"nested", "drop"}, update.DeleteField),
	}); err != nil {
		t.Fatalf("nested delete: %v", err)
	}
	if _, has := data["nested"].(map[string]any)["drop"]; has {
		t.Fatal("expected nested.drop to be deleted")
	}

	// 6. applyIncrement nested paths: missing intermediate (auto-created), existing map intermediate, float delta.
	data = map[string]any{
		"existingMap": map[string]any{"val": 10},
		"scalar":      "str",
	}
	if err := applyUpdates(data, []update.Update{
		update.ByFieldPath(update.FieldPath{"createdMap", "n"}, dal.Increment(5)),
		update.ByFieldPath(update.FieldPath{"existingMap", "val"}, stubTransform{name: "increment", val: 1.5}),
	}); err != nil {
		t.Fatalf("nested increment: %v", err)
	}
	if got := data["createdMap"].(map[string]any)["n"]; got != int64(5) {
		t.Errorf("createdMap.n = %v, want 5", got)
	}
	if got := data["existingMap"].(map[string]any)["val"]; got != 11.5 {
		t.Errorf("existingMap.val = %v, want 11.5", got)
	}

	// 7. applyIncrement errors: non-map intermediate, non-numeric delta, non-numeric existing target.
	if err := applyUpdates(data, []update.Update{
		update.ByFieldPath(update.FieldPath{"scalar", "n"}, dal.Increment(1)),
	}); err == nil {
		t.Fatal("expected error for increment through non-map intermediate")
	}
	if err := applyUpdates(data, []update.Update{
		update.ByFieldName("existingMap", stubTransform{name: "increment", val: "bad-delta"}),
	}); err == nil {
		t.Fatal("expected error for non-numeric increment delta")
	}
	if err := applyUpdates(data, []update.Update{
		update.ByFieldName("scalar", dal.Increment(1)),
	}); err == nil {
		t.Fatal("expected error for increment on non-numeric target field")
	}
}
