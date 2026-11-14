package dalgo2ghingitdb

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
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
	for _, want := range []string{`subcollection spaces/contacts of parent record "family"`, "spaces/family/contacts holds 1 file(s)",
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

// failIfMislaid passes on a configuration or listing error.
func TestFailIfMislaid_Errors(t *testing.T) {
	t.Parallel()
	check := &mislaidCheck{dir: "spaces/family/contacts"}
	if err := (readonlyTx{db: &githubDB{cfg: Config{}}}).failIfMislaid(context.Background(), check); err == nil {
		t.Error("invalid config: expected an error")
	}
	srv := newAPIErrorServer(t)
	defer srv.Close()
	tx := readonlyTx{db: &githubDB{cfg: Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: srv.URL + "/"}}}
	if err := tx.failIfMislaid(context.Background(), check); err == nil || errors.Is(err, ErrMislaidSubCollection) {
		t.Errorf("listing error: expected it to pass through, got %v", err)
	}
}
