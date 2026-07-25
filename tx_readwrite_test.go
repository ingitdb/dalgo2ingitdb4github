package dalgo2ghingitdb

import (
	"context"
	"errors"
	"testing"

	"github.com/dal-go/dalgo/dal"
	dalrecord "github.com/dal-go/record"
	"github.com/ingitdb/ingitdb-go/ingitdb"
)

func TestReadwriteTx_ID(t *testing.T) {
	t.Parallel()
	cfg := Config{Owner: "test", Repo: "test"}
	def := &ingitdb.Definition{Collections: map[string]*ingitdb.CollectionDef{}}
	db, err := NewGitHubDBWithDef(cfg, def)
	if err != nil {
		t.Fatalf("NewGitHubDBWithDef: %v", err)
	}
	ctx := context.Background()
	err = db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		id := tx.ID()
		if id != "" {
			t.Errorf("ID() = %q, want empty string", id)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RunReadwriteTransaction: %v", err)
	}
}

func TestReadwriteTx_SetMulti(t *testing.T) {
	t.Parallel()
	cfg := Config{Owner: "test", Repo: "test"}
	def := &ingitdb.Definition{Collections: map[string]*ingitdb.CollectionDef{}}
	db, err := NewGitHubDBWithDef(cfg, def)
	if err != nil {
		t.Fatalf("NewGitHubDBWithDef: %v", err)
	}
	ctx := context.Background()
	err = db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		records := []dalrecord.Record{}
		setMultiErr := tx.SetMulti(ctx, records)
		if setMultiErr == nil {
			t.Fatal("SetMulti() expected error, got nil")
		}
		if !errors.Is(setMultiErr, dal.ErrNotImplementedYet) {
			t.Errorf("SetMulti() error = %v, want dal.ErrNotImplementedYet", setMultiErr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RunReadwriteTransaction: %v", err)
	}
}

func TestReadwriteTx_DeleteMulti(t *testing.T) {
	t.Parallel()
	cfg := Config{Owner: "test", Repo: "test"}
	def := &ingitdb.Definition{Collections: map[string]*ingitdb.CollectionDef{}}
	db, err := NewGitHubDBWithDef(cfg, def)
	if err != nil {
		t.Fatalf("NewGitHubDBWithDef: %v", err)
	}
	ctx := context.Background()
	err = db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		keys := []*dalrecord.Key{}
		deleteMultiErr := tx.DeleteMulti(ctx, keys)
		if deleteMultiErr == nil {
			t.Fatal("DeleteMulti() expected error, got nil")
		}
		if !errors.Is(deleteMultiErr, dal.ErrNotImplementedYet) {
			t.Errorf("DeleteMulti() error = %v, want dal.ErrNotImplementedYet", deleteMultiErr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RunReadwriteTransaction: %v", err)
	}
}

func TestReadwriteTx_Update(t *testing.T) {
	t.Parallel()
	cfg := Config{Owner: "test", Repo: "test"}
	def := &ingitdb.Definition{Collections: map[string]*ingitdb.CollectionDef{}}
	db, err := NewGitHubDBWithDef(cfg, def)
	if err != nil {
		t.Fatalf("NewGitHubDBWithDef: %v", err)
	}
	ctx := context.Background()
	err = db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		key := dalrecord.NewKeyWithID("test", "test")
		// The collection is absent from the definition → the record cannot exist →
		// Update reports not-found (Update is not idempotent).
		updateErr := tx.Update(ctx, key, nil)
		if !dalrecord.IsNotFound(updateErr) {
			t.Fatalf("Update() error = %v, want not-found", updateErr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RunReadwriteTransaction: %v", err)
	}
}

func TestReadwriteTx_UpdateRecord(t *testing.T) {
	t.Parallel()
	cfg := Config{Owner: "test", Repo: "test"}
	def := &ingitdb.Definition{Collections: map[string]*ingitdb.CollectionDef{}}
	db, err := NewGitHubDBWithDef(cfg, def)
	if err != nil {
		t.Fatalf("NewGitHubDBWithDef: %v", err)
	}
	ctx := context.Background()
	err = db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		key := dalrecord.NewKeyWithID("test", "test")
		record := dalrecord.NewRecordWithData(key, map[string]any{})
		// Unknown collection → not-found (UpdateRecord delegates to Update).
		updateRecordErr := tx.UpdateRecord(ctx, record, nil)
		if !dalrecord.IsNotFound(updateRecordErr) {
			t.Fatalf("UpdateRecord() error = %v, want not-found", updateRecordErr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RunReadwriteTransaction: %v", err)
	}
}

func TestReadwriteTx_UpdateMulti(t *testing.T) {
	t.Parallel()
	cfg := Config{Owner: "test", Repo: "test"}
	def := &ingitdb.Definition{Collections: map[string]*ingitdb.CollectionDef{}}
	db, err := NewGitHubDBWithDef(cfg, def)
	if err != nil {
		t.Fatalf("NewGitHubDBWithDef: %v", err)
	}
	ctx := context.Background()
	err = db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		keys := []*dalrecord.Key{}
		updateMultiErr := tx.UpdateMulti(ctx, keys, nil)
		if updateMultiErr == nil {
			t.Fatal("UpdateMulti() expected error, got nil")
		}
		if !errors.Is(updateMultiErr, dal.ErrNotImplementedYet) {
			t.Errorf("UpdateMulti() error = %v, want dal.ErrNotImplementedYet", updateMultiErr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RunReadwriteTransaction: %v", err)
	}
}

func TestReadwriteTx_InsertMulti(t *testing.T) {
	t.Parallel()
	cfg := Config{Owner: "test", Repo: "test"}
	def := &ingitdb.Definition{Collections: map[string]*ingitdb.CollectionDef{}}
	db, err := NewGitHubDBWithDef(cfg, def)
	if err != nil {
		t.Fatalf("NewGitHubDBWithDef: %v", err)
	}
	ctx := context.Background()
	err = db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		records := []dalrecord.Record{}
		insertMultiErr := tx.InsertMulti(ctx, records)
		if insertMultiErr == nil {
			t.Fatal("InsertMulti() expected error, got nil")
		}
		if !errors.Is(insertMultiErr, dal.ErrNotImplementedYet) {
			t.Errorf("InsertMulti() error = %v, want dal.ErrNotImplementedYet", insertMultiErr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RunReadwriteTransaction: %v", err)
	}
}

func TestReadwriteTx_SetMapOfRecords(t *testing.T) {
	t.Parallel()
	fixtures := []githubFileFixture{{
		path:    "demo-dbs/todo/tags/tags.json",
		content: `{"active": {"title": "Active"}}`,
	}}
	server := newGitHubContentsServer(t, fixtures)
	defer server.Close()

	def := &ingitdb.Definition{
		Collections: map[string]*ingitdb.CollectionDef{
			"todo.tags": {
				ID:      "todo.tags",
				DirPath: "demo-dbs/todo/tags",
				RecordFile: &ingitdb.RecordFileDef{
					Name:       "tags.json",
					Format:     "json",
					RecordType: ingitdb.MapOfRecords,
				},
				Columns: map[string]*ingitdb.ColumnDef{
					"title": {Type: ingitdb.ColumnTypeString},
				},
			},
		},
	}
	cfg := Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: server.URL + "/"}
	db, err := NewGitHubDBWithDef(cfg, def)
	if err != nil {
		t.Fatalf("NewGitHubDBWithDef: %v", err)
	}

	key := dalrecord.NewKeyWithID("todo.tags", "active")
	data := map[string]any{"title": "Updated"}
	record := dalrecord.NewRecordWithData(key, data)
	ctx := context.Background()
	err = db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(ctx, record)
	})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
}

func TestReadwriteTx_SetMapOfRecords_NewFile(t *testing.T) {
	t.Parallel()
	fixtures := []githubFileFixture{}
	server := newGitHubContentsServer(t, fixtures)
	defer server.Close()

	def := &ingitdb.Definition{
		Collections: map[string]*ingitdb.CollectionDef{
			"todo.tags": {
				ID:      "todo.tags",
				DirPath: "demo-dbs/todo/tags",
				RecordFile: &ingitdb.RecordFileDef{
					Name:       "tags.json",
					Format:     "json",
					RecordType: ingitdb.MapOfRecords,
				},
				Columns: map[string]*ingitdb.ColumnDef{
					"title": {Type: ingitdb.ColumnTypeString},
				},
			},
		},
	}
	cfg := Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: server.URL + "/"}
	db, err := NewGitHubDBWithDef(cfg, def)
	if err != nil {
		t.Fatalf("NewGitHubDBWithDef: %v", err)
	}

	key := dalrecord.NewKeyWithID("todo.tags", "new")
	data := map[string]any{"title": "New"}
	record := dalrecord.NewRecordWithData(key, data)
	ctx := context.Background()
	err = db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(ctx, record)
	})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
}

func TestReadwriteTx_InsertMapOfRecords(t *testing.T) {
	t.Parallel()
	fixtures := []githubFileFixture{{
		path:    "demo-dbs/todo/tags/tags.json",
		content: `{"active": {"title": "Active"}}`,
	}}
	server := newGitHubContentsServer(t, fixtures)
	defer server.Close()

	def := &ingitdb.Definition{
		Collections: map[string]*ingitdb.CollectionDef{
			"todo.tags": {
				ID:      "todo.tags",
				DirPath: "demo-dbs/todo/tags",
				RecordFile: &ingitdb.RecordFileDef{
					Name:       "tags.json",
					Format:     "json",
					RecordType: ingitdb.MapOfRecords,
				},
				Columns: map[string]*ingitdb.ColumnDef{
					"title": {Type: ingitdb.ColumnTypeString},
				},
			},
		},
	}
	cfg := Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: server.URL + "/"}
	db, err := NewGitHubDBWithDef(cfg, def)
	if err != nil {
		t.Fatalf("NewGitHubDBWithDef: %v", err)
	}

	key := dalrecord.NewKeyWithID("todo.tags", "new")
	data := map[string]any{"title": "New"}
	record := dalrecord.NewRecordWithData(key, data)
	ctx := context.Background()
	err = db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Insert(ctx, record)
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
}

func TestReadwriteTx_InsertMapOfRecords_AlreadyExists(t *testing.T) {
	t.Parallel()
	fixtures := []githubFileFixture{{
		path:    "demo-dbs/todo/tags/tags.json",
		content: `{"active": {"title": "Active"}}`,
	}}
	server := newGitHubContentsServer(t, fixtures)
	defer server.Close()

	def := &ingitdb.Definition{
		Collections: map[string]*ingitdb.CollectionDef{
			"todo.tags": {
				ID:      "todo.tags",
				DirPath: "demo-dbs/todo/tags",
				RecordFile: &ingitdb.RecordFileDef{
					Name:       "tags.json",
					Format:     "json",
					RecordType: ingitdb.MapOfRecords,
				},
				Columns: map[string]*ingitdb.ColumnDef{
					"title": {Type: ingitdb.ColumnTypeString},
				},
			},
		},
	}
	cfg := Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: server.URL + "/"}
	db, err := NewGitHubDBWithDef(cfg, def)
	if err != nil {
		t.Fatalf("NewGitHubDBWithDef: %v", err)
	}

	key := dalrecord.NewKeyWithID("todo.tags", "active")
	data := map[string]any{"title": "Active"}
	record := dalrecord.NewRecordWithData(key, data)
	ctx := context.Background()
	err = db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Insert(ctx, record)
	})
	if err == nil {
		t.Fatal("Insert() expected error for existing record, got nil")
	}
	expectedMsg := "record already exists: todo.tags/active"
	if err.Error() != expectedMsg {
		t.Errorf("Insert() error = %q, want %q", err.Error(), expectedMsg)
	}
}

func TestReadwriteTx_DeleteMapOfRecords(t *testing.T) {
	t.Parallel()
	fixtures := []githubFileFixture{{
		path:    "demo-dbs/todo/tags/tags.json",
		content: `{"active": {"title": "Active"}}`,
	}}
	server := newGitHubContentsServer(t, fixtures)
	defer server.Close()

	def := &ingitdb.Definition{
		Collections: map[string]*ingitdb.CollectionDef{
			"todo.tags": {
				ID:      "todo.tags",
				DirPath: "demo-dbs/todo/tags",
				RecordFile: &ingitdb.RecordFileDef{
					Name:       "tags.json",
					Format:     "json",
					RecordType: ingitdb.MapOfRecords,
				},
				Columns: map[string]*ingitdb.ColumnDef{
					"title": {Type: ingitdb.ColumnTypeString},
				},
			},
		},
	}
	cfg := Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: server.URL + "/"}
	db, err := NewGitHubDBWithDef(cfg, def)
	if err != nil {
		t.Fatalf("NewGitHubDBWithDef: %v", err)
	}

	key := dalrecord.NewKeyWithID("todo.tags", "active")
	ctx := context.Background()
	err = db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Delete(ctx, key)
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestReadwriteTx_DeleteMapOfRecords_FileNotFound(t *testing.T) {
	t.Parallel()
	fixtures := []githubFileFixture{}
	server := newGitHubContentsServer(t, fixtures)
	defer server.Close()

	def := &ingitdb.Definition{
		Collections: map[string]*ingitdb.CollectionDef{
			"todo.tags": {
				ID:      "todo.tags",
				DirPath: "demo-dbs/todo/tags",
				RecordFile: &ingitdb.RecordFileDef{
					Name:       "tags.json",
					Format:     "json",
					RecordType: ingitdb.MapOfRecords,
				},
				Columns: map[string]*ingitdb.ColumnDef{
					"title": {Type: ingitdb.ColumnTypeString},
				},
			},
		},
	}
	cfg := Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: server.URL + "/"}
	db, err := NewGitHubDBWithDef(cfg, def)
	if err != nil {
		t.Fatalf("NewGitHubDBWithDef: %v", err)
	}

	key := dalrecord.NewKeyWithID("todo.tags", "active")
	ctx := context.Background()
	err = db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Delete(ctx, key)
	})
	if err != nil {
		t.Fatalf("Delete() expected ErrRecordNotFound, got %v", err)
	}
}

func TestReadwriteTx_DeleteMapOfRecords_RecordNotInMap(t *testing.T) {
	t.Parallel()
	fixtures := []githubFileFixture{{
		path:    "demo-dbs/todo/tags/tags.json",
		content: `{"active": {"title": "Active"}}`,
	}}
	server := newGitHubContentsServer(t, fixtures)
	defer server.Close()

	def := &ingitdb.Definition{
		Collections: map[string]*ingitdb.CollectionDef{
			"todo.tags": {
				ID:      "todo.tags",
				DirPath: "demo-dbs/todo/tags",
				RecordFile: &ingitdb.RecordFileDef{
					Name:       "tags.json",
					Format:     "json",
					RecordType: ingitdb.MapOfRecords,
				},
				Columns: map[string]*ingitdb.ColumnDef{
					"title": {Type: ingitdb.ColumnTypeString},
				},
			},
		},
	}
	cfg := Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: server.URL + "/"}
	db, err := NewGitHubDBWithDef(cfg, def)
	if err != nil {
		t.Fatalf("NewGitHubDBWithDef: %v", err)
	}

	key := dalrecord.NewKeyWithID("todo.tags", "nonexistent")
	ctx := context.Background()
	err = db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Delete(ctx, key)
	})
	if err != nil {
		t.Fatalf("Delete() expected ErrRecordNotFound, got %v", err)
	}
}

func TestEncodeRecordContent_YAML(t *testing.T) {
	t.Parallel()
	data := map[string]any{"title": "Test", "value": 123}
	encoded, err := encodeRecordContent(data, "yaml")
	if err != nil {
		t.Fatalf("encodeRecordContent(yaml): %v", err)
	}
	if len(encoded) == 0 {
		t.Error("encodeRecordContent(yaml) returned empty result")
	}
}

func TestEncodeRecordContent_JSON(t *testing.T) {
	t.Parallel()
	data := map[string]any{"title": "Test", "value": 123}
	encoded, err := encodeRecordContent(data, "json")
	if err != nil {
		t.Fatalf("encodeRecordContent(json): %v", err)
	}
	if len(encoded) == 0 {
		t.Error("encodeRecordContent(json) returned empty result")
	}
}

func TestEncodeRecordContent_UnsupportedFormat(t *testing.T) {
	t.Parallel()
	data := map[string]any{"title": "Test"}
	_, err := encodeRecordContent(data, "xml")
	if err == nil {
		t.Fatal("encodeRecordContent() expected error for unsupported format, got nil")
	}
	expectedMsg := `unsupported record format "xml"`
	if err.Error() != expectedMsg {
		t.Errorf("encodeRecordContent() error = %q, want %q", err.Error(), expectedMsg)
	}
}
