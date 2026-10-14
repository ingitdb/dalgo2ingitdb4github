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

func TestBatchingTx_Update_And_Commit_ErrorPaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	def := singleRecordDef("tags", "data/tags", "{key}.yaml")
	recordPath := seedFixturePath(def, "tags", "k")
	fixtures := map[string]string{
		recordPath: "count: 1\n",
	}
	srv := makeBatchServer(t, fixtures)
	defer srv.Close()

	cfg := Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: srv.URL + "/"}
	bdb, err := NewBatchingGitHubDB(cfg, def, "test: update")
	if err != nil {
		t.Fatalf("NewBatchingGitHubDB: %v", err)
	}

	newTx := func(db *githubDB) *batchingTx {
		return &batchingTx{
			readonlyTx:    readonlyTx{db: db},
			opts:          dal.NewTransactionOptions(dal.TxWithMessage("opt-msg")),
			bufferedFiles: make(map[string]TreeChange),
			workingMaps:   make(map[string]map[string]map[string]any),
			mapColDefs:    make(map[string]*ingitdb.CollectionDef),
			mapLoaded:     make(map[string]bool),
		}
	}

	tx := newTx(bdb.githubDB)
	if got := tx.Options().Message(); got != "opt-msg" {
		t.Errorf("tx.Options().Message() = %q, want opt-msg", got)
	}

	key := record.NewKeyWithID("tags", "k")

	// 1. Preconditions not supported.
	if err := tx.Update(ctx, key, nil, dal.WithExistsPrecondition()); err == nil {
		t.Fatal("expected error for preconditions")
	}

	// 2. Non-errCollectionNotInDefinition error from resolveCollection (nil key).
	if err := tx.Update(ctx, nil, nil); err == nil {
		t.Fatal("expected error for nil key")
	}

	// 3. SingleRecord applyUpdates error.
	if err := tx.Update(ctx, key, []update.Update{update.ByFieldName("count", stubTransform{name: "bad"})}); err == nil {
		t.Fatal("expected applyUpdates error on SingleRecord")
	}

	// 4. SingleRecord buffered as deletion -> ErrRecordNotFound.
	tx.bufferedFiles[recordPath] = TreeChange{Path: recordPath, Content: nil}
	if err := tx.Update(ctx, key, []update.Update{update.ByFieldName("count", 2)}); !record.IsNotFound(err) {
		t.Fatalf("expected ErrRecordNotFound for deleted buffered record, got %v", err)
	}

	// 5. SingleRecord buffered with invalid YAML -> parse error.
	tx.bufferedFiles[recordPath] = TreeChange{Path: recordPath, Content: []byte(":::\n\t:")}
	if err := tx.Update(ctx, key, []update.Update{update.ByFieldName("count", 2)}); err == nil {
		t.Fatal("expected parse error for invalid buffered YAML")
	}

	// 6. SingleRecord encodeRecordContent error (JSON format with unencodable chan value).
	jsonDef := &ingitdb.Definition{
		Collections: map[string]*ingitdb.CollectionDef{
			"tags": {
				ID:      "tags",
				DirPath: "data/tags",
				RecordFile: &ingitdb.RecordFileDef{
					Name:       "{key}.json",
					Format:     ingitdb.RecordFormatJSON,
					RecordType: ingitdb.SingleRecord,
				},
			},
		},
	}
	jsonBdb, err := NewBatchingGitHubDB(cfg, jsonDef, "msg")
	if err != nil {
		t.Fatalf("NewBatchingGitHubDB(json): %v", err)
	}
	jsonTx := newTx(jsonBdb.githubDB)
	jsonPath := seedFixturePath(jsonDef, "tags", "k")
	jsonTx.bufferedFiles[jsonPath] = TreeChange{Path: jsonPath, Content: []byte(`{"count": 1}`)}
	if err := jsonTx.Update(ctx, key, []update.Update{update.ByFieldName("bad", make(chan int))}); err == nil {
		t.Fatal("expected encodeRecordContent error on JSON channel value")
	}

	// 7. MapOfRecords ensureMapLoaded error and applyUpdates error.
	mapDef := mapRecordDef("tags", "data/tags", "tags.json")
	mapPath := resolveRecordPath(mapDef.Collections["tags"], "")
	mapSrv := makeBatchServer(t, map[string]string{
		mapPath: `{"active": {"count": 1}}`,
	})
	defer mapSrv.Close()
	mapCfg := Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: mapSrv.URL + "/"}
	mapBdb, err := NewBatchingGitHubDB(mapCfg, mapDef, "msg")
	if err != nil {
		t.Fatalf("NewBatchingGitHubDB(map): %v", err)
	}
	mapTx := newTx(mapBdb.githubDB)
	if err := mapTx.Update(ctx, record.NewKeyWithID("tags", "active"), []update.Update{
		update.ByFieldName("count", stubTransform{name: "bad"}),
	}); err == nil {
		t.Fatal("expected applyUpdates error on MapOfRecords")
	}

	// Closed server triggers ensureMapLoaded / CommitChanges error.
	closedSrv := makeBatchServer(t, nil)
	closedSrv.Close()
	closedCfg := Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: closedSrv.URL + "/"}
	closedMapBdb, err := NewBatchingGitHubDB(closedCfg, mapDef, "msg")
	if err != nil {
		t.Fatalf("NewBatchingGitHubDB(closed): %v", err)
	}
	closedMapTx := newTx(closedMapBdb.githubDB)
	if err := closedMapTx.Update(ctx, record.NewKeyWithID("tags", "active"), nil); err == nil {
		t.Fatal("expected ensureMapLoaded error on closed server")
	}

	// 8. RunReadwriteTransaction CommitChanges error on closed server.
	closedSingleBdb, err := NewBatchingGitHubDB(closedCfg, def, "msg")
	if err != nil {
		t.Fatalf("NewBatchingGitHubDB(closed single): %v", err)
	}
	if err := closedSingleBdb.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(ctx, readyRecord(key, map[string]any{"count": 1}))
	}); err == nil {
		t.Fatal("expected CommitChanges error on closed server")
	}
}
