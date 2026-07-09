package dalgo2ghingitdb

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path"
	"reflect"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
)

// queryMockServer mocks the subset of the GitHub API a structured query needs:
//   - Git Data API (GetRef / GetCommit / GetTree) so TreeWriter.ListFilesUnder
//     can enumerate the collection's record files.
//   - Contents API (GET) so fileReader.ReadFile can read each record blob.
//
// blobs maps repo-relative file paths to their raw (decoded) content. The tree
// is derived from the blob paths so a single fixture drives both endpoints.
type queryMockServer struct {
	owner  string
	repo   string
	branch string
	blobs  map[string]string
}

func newQueryTestServer(t *testing.T, blobs map[string]string) *httptest.Server {
	t.Helper()
	m := &queryMockServer{owner: "ingitdb", repo: "ingitdb-cli", branch: "main", blobs: blobs}
	return httptest.NewServer(http.HandlerFunc(m.serve))
}

func (m *queryMockServer) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	repoPrefix := "/api/v3/repos/" + m.owner + "/" + m.repo
	p := r.URL.Path
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/git/ref/heads/"+m.branch):
		writeJSON(w, map[string]any{
			"ref":    "refs/heads/" + m.branch,
			"object": map[string]any{"sha": "commit-sha", "type": "commit"},
		})
	case r.Method == http.MethodGet && strings.Contains(p, "/git/commits/commit-sha"):
		writeJSON(w, map[string]any{
			"sha":  "commit-sha",
			"tree": map[string]any{"sha": "tree-sha"},
		})
	case r.Method == http.MethodGet && strings.Contains(p, "/git/trees/tree-sha"):
		entries := make([]map[string]any, 0, len(m.blobs))
		for blobPath := range m.blobs {
			entries = append(entries, map[string]any{
				"path": blobPath,
				"mode": "100644",
				"type": "blob",
				"sha":  "sha-" + blobPath,
			})
		}
		writeJSON(w, map[string]any{
			"sha":       "tree-sha",
			"tree":      entries,
			"truncated": false,
		})
	case r.Method == http.MethodGet && strings.HasPrefix(p, repoPrefix+"/contents/"):
		requested := strings.TrimPrefix(p, repoPrefix+"/contents/")
		content, ok := m.blobs[requested]
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]any{
			"type":     "file",
			"encoding": "base64",
			"content":  base64.StdEncoding.EncodeToString([]byte(content)),
			"sha":      "sha-" + requested,
			"name":     path.Base(requested),
			"path":     requested,
		})
	default:
		http.Error(w, "unexpected request: "+r.Method+" "+p, http.StatusNotImplemented)
	}
}

func writeJSON(w http.ResponseWriter, body any) {
	_ = json.NewEncoder(w).Encode(body)
}

// newQueryTestDB builds a githubDB pointed at the mock server for a single
// YAML single-record collection named "countries" stored under data/countries.
func newQueryTestDB(t *testing.T, srv *httptest.Server) dal.DB {
	t.Helper()
	def := buildSingleRecordDef("countries", "data/countries", "{key}.yaml")
	cfg := Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: srv.URL + "/"}
	db, err := NewGitHubDBWithDef(cfg, def)
	if err != nil {
		t.Fatalf("NewGitHubDBWithDef: %v", err)
	}
	return db
}

// countriesBlobs returns three single-record YAML files under the collection's
// $records directory. Path shape mirrors resolveRecordPath:
// data/countries/$records/{key}.yaml.
func countriesBlobs() map[string]string {
	return map[string]string{
		"data/countries/$records/us.yaml": "name: United States\npopulation: 331000000\n",
		"data/countries/$records/gb.yaml": "name: United Kingdom\npopulation: 67000000\n",
		"data/countries/$records/ie.yaml": "name: Ireland\npopulation: 5000000\n",
		// A README at the collection root must NOT be scanned as a record: it is
		// outside the $records base directory.
		"data/countries/README.md": "# Countries\n",
	}
}

func runQuery(t *testing.T, db dal.DB, q dal.Query) []dal.Record {
	t.Helper()
	var out []dal.Record
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
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	return out
}

func recordIDs(records []dal.Record) []string {
	ids := make([]string, len(records))
	for i, rec := range records {
		ids[i] = rec.Key().ID.(string)
	}
	return ids
}

func TestQuery_SelectAll(t *testing.T) {
	t.Parallel()
	srv := newQueryTestServer(t, countriesBlobs())
	defer srv.Close()
	db := newQueryTestDB(t, srv)

	q := dal.From(dal.NewRootCollectionRef("countries", "")).NewQuery().
		SelectIntoRecord(func() dal.Record {
			return dal.NewRecordWithIncompleteKey("countries", reflect.String, map[string]any{})
		})
	records := runQuery(t, db, q)

	if len(records) != 3 {
		t.Fatalf("SELECT *: got %d records, want 3 (ids=%v)", len(records), recordIDs(records))
	}
	// README.md must be excluded from the record set.
	for _, id := range recordIDs(records) {
		if id == "README" {
			t.Errorf("README.md was scanned as a record")
		}
	}
	// Data must decode into the record map.
	byID := map[string]map[string]any{}
	for _, rec := range records {
		byID[rec.Key().ID.(string)] = rec.Data().(map[string]any)
	}
	if got := byID["us"]["name"]; got != "United States" {
		t.Errorf("us.name = %v, want United States", got)
	}
}

func TestQuery_WhereFieldEquals(t *testing.T) {
	t.Parallel()
	srv := newQueryTestServer(t, countriesBlobs())
	defer srv.Close()
	db := newQueryTestDB(t, srv)

	q := dal.From(dal.NewRootCollectionRef("countries", "")).NewQuery().
		WhereField("name", dal.Equal, "Ireland").
		SelectIntoRecord(func() dal.Record {
			return dal.NewRecordWithIncompleteKey("countries", reflect.String, map[string]any{})
		})
	records := runQuery(t, db, q)

	if got := recordIDs(records); len(got) != 1 || got[0] != "ie" {
		t.Fatalf("WHERE name==Ireland: got ids %v, want [ie]", got)
	}
}

func TestQuery_OrderByAscendingAndDescending(t *testing.T) {
	t.Parallel()
	srv := newQueryTestServer(t, countriesBlobs())
	defer srv.Close()
	db := newQueryTestDB(t, srv)

	asc := dal.From(dal.NewRootCollectionRef("countries", "")).NewQuery().
		OrderBy(dal.AscendingField("population")).
		SelectIntoRecord(func() dal.Record {
			return dal.NewRecordWithIncompleteKey("countries", reflect.String, map[string]any{})
		})
	if got := recordIDs(runQuery(t, db, asc)); !reflect.DeepEqual(got, []string{"ie", "gb", "us"}) {
		t.Errorf("ORDER BY population ASC: got %v, want [ie gb us]", got)
	}

	desc := dal.From(dal.NewRootCollectionRef("countries", "")).NewQuery().
		OrderBy(dal.DescendingField("population")).
		SelectIntoRecord(func() dal.Record {
			return dal.NewRecordWithIncompleteKey("countries", reflect.String, map[string]any{})
		})
	if got := recordIDs(runQuery(t, db, desc)); !reflect.DeepEqual(got, []string{"us", "gb", "ie"}) {
		t.Errorf("ORDER BY population DESC: got %v, want [us gb ie]", got)
	}
}

func TestQuery_Limit(t *testing.T) {
	t.Parallel()
	srv := newQueryTestServer(t, countriesBlobs())
	defer srv.Close()
	db := newQueryTestDB(t, srv)

	q := dal.From(dal.NewRootCollectionRef("countries", "")).NewQuery().
		OrderBy(dal.DescendingField("population")).
		Limit(2).
		SelectIntoRecord(func() dal.Record {
			return dal.NewRecordWithIncompleteKey("countries", reflect.String, map[string]any{})
		})
	if got := recordIDs(runQuery(t, db, q)); !reflect.DeepEqual(got, []string{"us", "gb"}) {
		t.Errorf("LIMIT 2 over DESC population: got %v, want [us gb]", got)
	}
}

func TestQuery_KeysOnly(t *testing.T) {
	t.Parallel()
	srv := newQueryTestServer(t, countriesBlobs())
	defer srv.Close()
	db := newQueryTestDB(t, srv)

	q := dal.From(dal.NewRootCollectionRef("countries", "")).NewQuery().
		OrderBy(dal.AscendingField("$id")).
		SelectKeysOnly(reflect.String)
	records := runQuery(t, db, q)

	if got := recordIDs(records); !reflect.DeepEqual(got, []string{"gb", "ie", "us"}) {
		t.Fatalf("keys-only ORDER BY $id: got %v, want [gb ie us]", got)
	}
	// Keys-only records carry no data map.
	for _, rec := range records {
		if data, ok := rec.Data().(map[string]any); ok && len(data) > 0 {
			t.Errorf("keys-only record %v unexpectedly carries data %v", rec.Key().ID, data)
		}
	}
}

func TestQuery_UnknownCollectionReturnsEmpty(t *testing.T) {
	t.Parallel()
	srv := newQueryTestServer(t, countriesBlobs())
	defer srv.Close()
	db := newQueryTestDB(t, srv)

	q := dal.From(dal.NewRootCollectionRef("cities", "")).NewQuery().
		SelectIntoRecord(func() dal.Record {
			return dal.NewRecordWithIncompleteKey("cities", reflect.String, map[string]any{})
		})
	if records := runQuery(t, db, q); len(records) != 0 {
		t.Fatalf("unknown collection: got %d records, want 0", len(records))
	}
}

func TestQuery_UnsupportedClausesReturnNotSupported(t *testing.T) {
	t.Parallel()
	srv := newQueryTestServer(t, countriesBlobs())
	defer srv.Close()
	db := newQueryTestDB(t, srv)

	newBase := func() dal.IQueryBuilder {
		return dal.From(dal.NewRootCollectionRef("countries", "")).NewQuery()
	}
	into := func() dal.Record {
		return dal.NewRecordWithIncompleteKey("countries", reflect.String, map[string]any{})
	}
	cases := map[string]dal.Query{
		"offset":     newBase().Offset(1).SelectIntoRecord(into),
		"startFrom":  newBase().StartFrom(dal.Cursor("x")).SelectIntoRecord(into),
		"groupBy":    newBase().GroupBy(dal.Field("name")).SelectIntoRecord(into),
		"selectCols": newBase().SelectColumns(dal.Column{Expression: dal.Field("name")}),
	}
	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			err := db.RunReadonlyTransaction(ctx, func(ctx context.Context, tx dal.ReadTransaction) error {
				_, err := tx.ExecuteQueryToRecordsReader(ctx, q)
				return err
			})
			if err == nil {
				t.Fatalf("%s: expected ErrNotSupported, got nil", name)
			}
			if !isNotSupported(err) {
				t.Errorf("%s: error %v is not dal.ErrNotSupported", name, err)
			}
		})
	}
}

func TestQuery_RecordsetReaderNotSupported(t *testing.T) {
	t.Parallel()
	srv := newQueryTestServer(t, countriesBlobs())
	defer srv.Close()
	db := newQueryTestDB(t, srv)

	q := dal.From(dal.NewRootCollectionRef("countries", "")).NewQuery().
		SelectIntoRecordset()
	err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
		_, err := tx.ExecuteQueryToRecordsetReader(ctx, q)
		return err
	})
	if !isNotSupported(err) {
		t.Fatalf("recordset reader: error %v is not dal.ErrNotSupported", err)
	}
}

func isNotSupported(err error) bool {
	return errors.Is(err, dal.ErrNotSupported)
}
