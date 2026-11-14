package dalgo2ghingitdb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/ingitdb/ingitdb-go/ingitdb"
)

// The definition puts the contacts of space "family" in spaces' records
// directory, next to its record file: spaces/$records/family/contacts/ under
// the default records_dir, spaces/family/contacts/ with records_dir ".".
func TestDefinedLayout_InstanceDir(t *testing.T) {
	t.Parallel()
	def := spacesWithContactsSubDef()
	parent := record.NewKeyWithID("spaces", "family")
	got, err := resolveScopedCollection(def, "contacts", parent)
	if err != nil {
		t.Fatal(err)
	}
	if got.DirPath != "spaces/$records/family/contacts" {
		t.Errorf("default records_dir: DirPath = %q", got.DirPath)
	}
	dot := "."
	def.Collections["spaces"].RecordFile.RecordsDir = &dot
	if got, _ = resolveScopedCollection(def, "contacts", parent); got.DirPath != "spaces/family/contacts" {
		t.Errorf("records_dir .: DirPath = %q", got.DirPath)
	}
	def.Collections["spaces"].RecordFile = nil
	if got, _ = resolveScopedCollection(def, "contacts", parent); got.DirPath != "spaces/family/contacts" {
		t.Errorf("no record file: DirPath = %q", got.DirPath)
	}
}

func queryContacts(db dal.DB, space string) ([]record.Record, error) {
	q := dal.From(dal.NewCollectionRef("contacts", "", record.NewKeyWithID("spaces", space))).NewQuery().SelectIntoRecord(func() record.Record {
		return record.NewRecordWithIncompleteKey("contacts", reflect.String, map[string]any{})
	})
	var out []record.Record
	err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
		reader, err := tx.ExecuteQueryToRecordsReader(ctx, q)
		if err != nil {
			return err
		}
		defer func() { _ = reader.Close() }()
		for {
			rec, err := reader.Next()
			if err == dal.ErrNoMoreRecords {
				return nil
			}
			if err != nil {
				return err
			}
			out = append(out, rec)
		}
	})
	return out, err
}

// A query of an instance that is empty where the definition puts it, while
// files exist where earlier versions wrote it, fails with the details instead
// of returning no rows. An instance with records, or with nothing anywhere,
// is unaffected.
func TestDefinedLayout_MislaidInstanceFailsQuery(t *testing.T) {
	t.Parallel()
	srv := newQueryTestServer(t, map[string]string{
		"spaces/family/contacts/$records/c1.yaml":        "name: Alice\n", // mislaid
		"spaces/$records/work/contacts/$records/c2.yaml": "name: Bob\n",
		"spaces/$records/school/contacts/$records/.keep": "",
	})
	defer srv.Close()
	cfg := Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: srv.URL + "/"}
	db, err := NewGitHubDBWithDef(cfg, spacesWithContactsSubDef())
	if err != nil {
		t.Fatal(err)
	}
	_, err = queryContacts(db, "family")
	if !errors.Is(err, ErrMislaidSubCollection) {
		t.Fatalf("expected ErrMislaidSubCollection, got %v", err)
	}
	for _, want := range []string{`subcollection spaces/contacts of parent record "family"`, "spaces/family/contacts holds 1 entr(ies)",
		"but the definition puts them at spaces/$records/family/contacts", `records_dir "$records"`, `records_dir: "."`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if rows, err := queryContacts(db, "work"); err != nil || len(rows) != 1 {
		t.Errorf("work: %d rows, %v", len(rows), err)
	}
	if rows, err := queryContacts(db, "nobody"); err != nil || len(rows) != 0 {
		t.Errorf("nobody: %d rows, %v", len(rows), err)
	}
}

// failIfMislaid passes a listing error through.
func TestFailIfMislaid_Errors(t *testing.T) {
	t.Parallel()
	srv := newAPIErrorServer(t)
	defer srv.Close()
	reader, err := NewGitHubFileReader(Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: srv.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	tx := readonlyTx{db: &githubDB{fileReader: reader.(*githubFileReader)}}
	if err := tx.failIfMislaid(context.Background(), []*mislaidCheck{{dir: "spaces/family/contacts"}}); err == nil || errors.Is(err, ErrMislaidSubCollection) {
		t.Errorf("listing error: expected it to pass through, got %v", err)
	}
}

// spacesContactsNotesDef is spaces > contacts > notes, every level keeping
// its records in $records.
func spacesContactsNotesDef() *ingitdb.Definition {
	def := spacesWithContactsSubDef()
	contacts := def.Collections["spaces"].SubCollections["contacts"]
	notes := *contacts
	notes.ID, notes.SubCollections = "notes", nil
	contacts.SubCollections = map[string]*ingitdb.CollectionDef{"notes": &notes}
	return def
}

func mockDB(t *testing.T, ref string, def *ingitdb.Definition, blobs map[string]string) dal.DB {
	t.Helper()
	m := &queryMockServer{owner: "ingitdb", repo: "ingitdb-cli", branch: "main", blobs: blobs, ref: ref}
	srv := httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(srv.Close)
	db, err := NewGitHubDBWithDef(Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: ref, APIBaseURL: srv.URL + "/"}, def)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// A Get of a record missing where the definition puts it, while the instance
// has data where earlier versions wrote it, fails with the details; writes
// fail before writing. The listing reads the configured Ref through the
// Contents API, so a tag works (the mock serves no branch named v1.0.0).
func TestDefinedLayout_MislaidInstanceFailsGetAndWrites(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := mockDB(t, "v1.0.0", spacesWithContactsSubDef(), map[string]string{
		"spaces/family/contacts/$records/c1.yaml": "name: Alice\n", // mislaid
	})
	key := record.NewKeyWithParentAndID(record.NewKeyWithID("spaces", "family"), "contacts", "c1")
	err := db.Get(ctx, record.NewRecordWithData(key, map[string]any{}))
	if !errors.Is(err, ErrMislaidSubCollection) || !strings.Contains(err.Error(), "spaces/family/contacts holds 1 entr(ies)") {
		t.Fatalf("Get: expected ErrMislaidSubCollection, got %v", err)
	}
	for name, write := range map[string]func(context.Context, dal.ReadwriteTransaction) error{
		"Set": func(_ context.Context, tx dal.ReadwriteTransaction) error {
			return tx.Set(ctx, record.NewRecordWithData(key, map[string]any{"name": "B"}))
		},
		"Insert": func(_ context.Context, tx dal.ReadwriteTransaction) error {
			return tx.Insert(ctx, record.NewRecordWithData(key, map[string]any{"name": "B"}))
		},
		"Delete": func(_ context.Context, tx dal.ReadwriteTransaction) error { return tx.Delete(ctx, key) },
	} {
		if err := db.RunReadwriteTransaction(ctx, write); !errors.Is(err, ErrMislaidSubCollection) {
			t.Errorf("%s: expected ErrMislaidSubCollection, got %v", name, err)
		}
	}
	// A record at the defined place is read without a listing.
	ok := mockDB(t, "v1.0.0", spacesWithContactsSubDef(), map[string]string{
		"spaces/$records/work/contacts/$records/c2.yaml": "name: Bob\n",
	})
	rec := record.NewRecordWithData(record.NewKeyWithParentAndID(record.NewKeyWithID("spaces", "work"), "contacts", "c2"), map[string]any{})
	if err := ok.Get(ctx, rec); err != nil || !rec.Exists() {
		t.Fatalf("Get work/c2: exists=%v, %v", rec.Exists(), err)
	}
}

// Mislaid data below a correctly placed ancestor is found, and so is the
// layout earlier versions wrote at every level.
func TestDefinedLayout_MislaidAtEveryLevel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	key := record.NewKeyWithParentAndID(
		record.NewKeyWithParentAndID(record.NewKeyWithID("spaces", "family"), "contacts", "c1"), "notes", "n1")
	for name, tc := range map[string]struct{ blob, want string }{
		"depth two":  {"spaces/$records/family/contacts/c1/notes/$records/n1.yaml", "subcollection contacts/notes of parent record \"c1\""},
		"old layout": {"spaces/family/contacts/c1/notes/n1.yaml", "subcollection spaces/contacts of parent record \"family\""},
	} {
		db := mockDB(t, "main", spacesContactsNotesDef(), map[string]string{tc.blob: "name: N\n"})
		err := db.Get(ctx, record.NewRecordWithData(key, map[string]any{}))
		if !errors.Is(err, ErrMislaidSubCollection) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: expected %q, got %v", name, tc.want, err)
		}
	}
}

// A nested records_dir is not mistaken for mislaid data, and a parent ID that
// climbs out of the collection directory is refused.
func TestDefinedLayout_NestedRecordsDirAndEscapingKeys(t *testing.T) {
	t.Parallel()
	def := spacesWithContactsSubDef()
	nested := "data/items"
	def.Collections["spaces"].RecordFile.RecordsDir = &nested
	col, checks, err := resolveScopedInstance(def, "contacts", record.NewKeyWithID("spaces", "data"))
	if err != nil || len(checks) != 0 || col.DirPath != "spaces/data/items/data/contacts" {
		t.Fatalf("records_dir data/items, parent data: %v, %d checks, %v", col, len(checks), err)
	}
	for _, dir := range []*string{nil, func() *string { s := "."; return &s }()} {
		def.Collections["spaces"].RecordFile.RecordsDir = dir
		if _, _, err := resolveScopedInstance(def, "contacts", record.NewKeyWithID("spaces", "..")); !errors.Is(err, ErrParentKeyEscapes) {
			t.Errorf("records_dir %v: expected ErrParentKeyEscapes, got %v", dir, err)
		}
	}
	deep := record.NewKeyWithParentAndID(record.NewKeyWithID("spaces", ".."), "contacts", "c1")
	if _, _, err := resolveScopedInstance(spacesContactsNotesDef(), "notes", deep); !errors.Is(err, ErrParentKeyEscapes) {
		t.Errorf("escaping ancestor: expected ErrParentKeyEscapes, got %v", err)
	}
	if !within("", "a") || within("", "..") || within(".", "../a") || !within("a", "a/b") || within("a", "ab") {
		t.Error("within misjudges a path")
	}
}

// A collection that declares a key encoding is refused, at any level, until
// this driver encodes keys.
func TestKeyEncodingRefused(t *testing.T) {
	t.Parallel()
	for name, setup := range map[string]func(*ingitdb.Definition){
		"root": func(d *ingitdb.Definition) {
			d.Collections["spaces"].RecordFile.KeyEncoding = ingitdb.KeyEncodingTildeV1
		},
		"target": func(d *ingitdb.Definition) {
			d.Collections["spaces"].SubCollections["contacts"].RecordFile.KeyEncoding = ingitdb.KeyEncodingTildeV1
		},
	} {
		def := spacesContactsNotesDef()
		setup(def)
		for _, c := range []struct {
			col    string
			parent *record.Key
		}{
			{"spaces", nil},
			{"contacts", record.NewKeyWithID("spaces", "s")},
			{"notes", record.NewKeyWithParentAndID(record.NewKeyWithID("spaces", "s"), "contacts", "c")},
		} {
			_, _, err := resolveScopedInstance(def, c.col, c.parent)
			wantRefused := name == "root" || c.col != "spaces"
			if refused := errors.Is(err, dal.ErrNotSupported); refused != wantRefused {
				t.Errorf("%s encoded, %s: %v", name, c.col, err)
			}
		}
	}
}
