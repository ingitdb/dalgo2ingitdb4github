package dalgo2ghingitdb

import (
	"context"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record/update"

	"github.com/dal-go/record"
	"github.com/ingitdb/ingitdb-go/ingitdb"
)

// seedFixturePath returns the repo path where a single-record key is stored, so
// tests can seed the tree/blob fixtures the batching DB reads through.
func seedFixturePath(def *ingitdb.Definition, collection, id string) string {
	colDef := def.Collections[collection]
	return resolveRecordPath(colDef, id)
}

// runBatchUpdate seeds the record at (collection,id) with seedYAML, runs a
// batching Update with the given updates, and returns the buffered SingleRecord
// content that would be committed (or an error).
func runBatchUpdate(t *testing.T, collection, id, seedYAML string, updates []update.Update) ([]byte, error) {
	t.Helper()
	def := singleRecordDef(collection, "data/"+collection, "{key}.yaml")
	fixtures := map[string]string{
		seedFixturePath(def, collection, id): seedYAML,
	}
	srv := makeBatchServer(t, fixtures)
	t.Cleanup(srv.Close)

	cfg := Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: srv.URL + "/"}
	bdb, err := NewBatchingGitHubDB(cfg, def, "test: update")
	if err != nil {
		t.Fatalf("NewBatchingGitHubDB: %v", err)
	}

	// Grab the buffered change directly from the tx so we can inspect the encoded
	// content without committing.
	var buffered []byte
	var updErr error
	txOuter := &batchingTx{
		readonlyTx:    readonlyTx{db: bdb.githubDB},
		bufferedFiles: make(map[string]TreeChange),
		workingMaps:   make(map[string]map[string]map[string]any),
		mapColDefs:    make(map[string]*ingitdb.CollectionDef),
		mapLoaded:     make(map[string]bool),
	}
	updErr = txOuter.Update(context.Background(), record.NewKeyWithID(collection, id), updates)
	if updErr == nil {
		buffered = txOuter.bufferedFiles[seedFixturePath(def, collection, id)].Content
	}
	return buffered, updErr
}

func TestBatchingTx_Update_SetField(t *testing.T) {
	t.Parallel()
	content, err := runBatchUpdate(t, "tags", "k", "title: Old\ncount: 1\n",
		[]update.Update{update.ByFieldName("title", "New")})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !strings.Contains(string(content), "title: New") {
		t.Errorf("buffered content = %q, want title updated to New", content)
	}
	if !strings.Contains(string(content), "count: 1") {
		t.Errorf("buffered content = %q, want count preserved", content)
	}
}

func TestBatchingTx_Update_NestedPath(t *testing.T) {
	t.Parallel()
	content, err := runBatchUpdate(t, "tags", "k", "title: X\n",
		[]update.Update{update.ByFieldPath(update.FieldPath{"meta", "color"}, "blue")})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	s := string(content)
	if !strings.Contains(s, "meta:") || !strings.Contains(s, "color: blue") {
		t.Errorf("buffered content = %q, want nested meta.color=blue", s)
	}
}

func TestBatchingTx_Update_DeleteField(t *testing.T) {
	t.Parallel()
	content, err := runBatchUpdate(t, "tags", "k", "title: X\nobsolete: gone\n",
		[]update.Update{update.DeleteByFieldName("obsolete")})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if strings.Contains(string(content), "obsolete") {
		t.Errorf("buffered content = %q, want obsolete field deleted", content)
	}
	if !strings.Contains(string(content), "title: X") {
		t.Errorf("buffered content = %q, want title preserved", content)
	}
}

func TestBatchingTx_Update_Increment(t *testing.T) {
	t.Parallel()
	content, err := runBatchUpdate(t, "tags", "k", "count: 5\n",
		[]update.Update{update.ByFieldName("count", dal.Increment(3))})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !strings.Contains(string(content), "count: 8") {
		t.Errorf("buffered content = %q, want count incremented to 8", content)
	}
}

func TestBatchingTx_Update_IncrementMissingField(t *testing.T) {
	t.Parallel()
	// Missing field counts as 0, so increment by 4 yields 4.
	content, err := runBatchUpdate(t, "tags", "k", "title: X\n",
		[]update.Update{update.ByFieldName("count", dal.Increment(4))})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !strings.Contains(string(content), "count: 4") {
		t.Errorf("buffered content = %q, want count=4 for missing-field increment", content)
	}
}

func TestBatchingTx_Update_ServerTimestamp(t *testing.T) {
	t.Parallel()
	content, err := runBatchUpdate(t, "tags", "k", "title: X\n",
		[]update.Update{update.ByFieldName("updatedAt", update.ServerTimestamp)})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !strings.Contains(string(content), "updatedAt:") {
		t.Errorf("buffered content = %q, want updatedAt set to an RFC3339 timestamp", content)
	}
	// Loosely assert the value looks like an RFC3339 timestamp (contains a 'T').
	if !strings.Contains(string(content), "T") {
		t.Errorf("buffered content = %q, want RFC3339-ish timestamp value", content)
	}
}

func TestBatchingTx_Update_MissingRecord_NotFound(t *testing.T) {
	t.Parallel()
	// No fixture seeded → the record is absent in the head tree → Update must
	// return ErrRecordNotFound (Update is not idempotent).
	def := singleRecordDef("tags", "data/tags", "{key}.yaml")
	srv := makeBatchServer(t, nil)
	defer srv.Close()
	cfg := Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: srv.URL + "/"}
	bdb, err := NewBatchingGitHubDB(cfg, def, "test: update")
	if err != nil {
		t.Fatalf("NewBatchingGitHubDB: %v", err)
	}
	upErr := bdb.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Update(ctx, record.NewKeyWithID("tags", "missing"),
			[]update.Update{update.ByFieldName("title", "X")})
	})
	if !record.IsNotFound(upErr) {
		t.Fatalf("Update missing record error = %v, want not-found", upErr)
	}
}

// TestBatchingTx_Update_MapOfRecords verifies Update on a MapOfRecords
// collection: it loads the map, mutates the target entry in place, and leaves
// the working map ready to flush.
func TestBatchingTx_Update_MapOfRecords(t *testing.T) {
	t.Parallel()
	def := mapRecordDef("tags", "data/tags", "tags.json")
	recordPath := resolveRecordPath(def.Collections["tags"], "")
	fixtures := map[string]string{
		recordPath: `{"active": {"title": "Old", "count": 5}, "archived": {"title": "Keep"}}`,
	}
	srv := makeBatchServer(t, fixtures)
	defer srv.Close()
	cfg := Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: srv.URL + "/"}
	bdb, err := NewBatchingGitHubDB(cfg, def, "test: update")
	if err != nil {
		t.Fatalf("NewBatchingGitHubDB: %v", err)
	}

	tx := &batchingTx{
		readonlyTx:    readonlyTx{db: bdb.githubDB},
		bufferedFiles: make(map[string]TreeChange),
		workingMaps:   make(map[string]map[string]map[string]any),
		mapColDefs:    make(map[string]*ingitdb.CollectionDef),
		mapLoaded:     make(map[string]bool),
	}
	ctx := context.Background()
	if upErr := tx.Update(ctx, record.NewKeyWithID("tags", "active"), []update.Update{
		update.ByFieldName("title", "New"),
		update.ByFieldName("count", dal.Increment(1)),
	}); upErr != nil {
		t.Fatalf("Update: %v", upErr)
	}

	active := tx.workingMaps[recordPath]["active"]
	if active["title"] != "New" {
		t.Errorf("active.title = %v, want New", active["title"])
	}
	// The other entry must be untouched.
	if tx.workingMaps[recordPath]["archived"]["title"] != "Keep" {
		t.Errorf("archived.title changed: %v", tx.workingMaps[recordPath]["archived"])
	}

	// Updating a missing entry must be not-found.
	if upErr := tx.Update(ctx, record.NewKeyWithID("tags", "ghost"),
		[]update.Update{update.ByFieldName("title", "X")}); !record.IsNotFound(upErr) {
		t.Fatalf("Update missing map entry = %v, want not-found", upErr)
	}
}

// TestBatchingTx_Update_PrefersBufferedContent verifies that an Update after a
// Set in the same tx reads the just-Set (buffered) content, not the repo.
func TestBatchingTx_Update_PrefersBufferedContent(t *testing.T) {
	t.Parallel()
	def := singleRecordDef("tags", "data/tags", "{key}.yaml")
	// Repo has count: 100, but the tx will Set count: 1 first, then increment.
	fixtures := map[string]string{
		seedFixturePath(def, "tags", "k"): "count: 100\n",
	}
	srv := makeBatchServer(t, fixtures)
	defer srv.Close()
	cfg := Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: srv.URL + "/"}
	bdb, err := NewBatchingGitHubDB(cfg, def, "test: update")
	if err != nil {
		t.Fatalf("NewBatchingGitHubDB: %v", err)
	}

	tx := &batchingTx{
		readonlyTx:    readonlyTx{db: bdb.githubDB},
		bufferedFiles: make(map[string]TreeChange),
		workingMaps:   make(map[string]map[string]map[string]any),
		mapColDefs:    make(map[string]*ingitdb.CollectionDef),
		mapLoaded:     make(map[string]bool),
	}
	ctx := context.Background()
	if setErr := tx.Set(ctx, readyRecord(record.NewKeyWithID("tags", "k"), map[string]any{"count": 1})); setErr != nil {
		t.Fatalf("Set: %v", setErr)
	}
	if upErr := tx.Update(ctx, record.NewKeyWithID("tags", "k"),
		[]update.Update{update.ByFieldName("count", dal.Increment(1))}); upErr != nil {
		t.Fatalf("Update: %v", upErr)
	}
	buffered := tx.bufferedFiles[seedFixturePath(def, "tags", "k")].Content
	if !strings.Contains(string(buffered), "count: 2") {
		t.Errorf("buffered content = %q, want count=2 (1 buffered + 1), not based on repo's 100", buffered)
	}
}
