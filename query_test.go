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
	"github.com/dal-go/record"
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

func runQuery(t *testing.T, db dal.DB, q dal.Query) []record.Record {
	t.Helper()
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
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	return out
}

func recordIDs(records []record.Record) []string {
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
		SelectIntoRecord(func() record.Record {
			return record.NewRecordWithIncompleteKey("countries", reflect.String, map[string]any{})
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
		SelectIntoRecord(func() record.Record {
			return record.NewRecordWithIncompleteKey("countries", reflect.String, map[string]any{})
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
		SelectIntoRecord(func() record.Record {
			return record.NewRecordWithIncompleteKey("countries", reflect.String, map[string]any{})
		})
	if got := recordIDs(runQuery(t, db, asc)); !reflect.DeepEqual(got, []string{"ie", "gb", "us"}) {
		t.Errorf("ORDER BY population ASC: got %v, want [ie gb us]", got)
	}

	desc := dal.From(dal.NewRootCollectionRef("countries", "")).NewQuery().
		OrderBy(dal.DescendingField("population")).
		SelectIntoRecord(func() record.Record {
			return record.NewRecordWithIncompleteKey("countries", reflect.String, map[string]any{})
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
		SelectIntoRecord(func() record.Record {
			return record.NewRecordWithIncompleteKey("countries", reflect.String, map[string]any{})
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
		SelectIntoRecord(func() record.Record {
			return record.NewRecordWithIncompleteKey("cities", reflect.String, map[string]any{})
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
	into := func() record.Record {
		return record.NewRecordWithIncompleteKey("countries", reflect.String, map[string]any{})
	}
	// Supply a matching GROUP BY so DALgo accepts HAVING before the adapter rejects it.
	cases := map[string]dal.Query{
		"offset":     newBase().Offset(1).SelectIntoRecord(into),
		"startFrom":  newBase().StartFrom(dal.Cursor("x")).SelectIntoRecord(into),
		"groupBy":    newBase().GroupBy(dal.Field("name")).SelectIntoRecord(into),
		"having":     newBase().GroupBy(dal.Field("name")).Having(dal.WhereField("name", dal.Equal, "x")).SelectIntoRecord(into),
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

type customFromStructuredQuery struct {
	dal.StructuredQuery
	from dal.FromSource
}

func (q customFromStructuredQuery) From() dal.FromSource { return q.from }

type nilBaseFromSource struct {
	dal.FromSource
}

func (nilBaseFromSource) Base() dal.RecordsetSource { return nil }

type stubCondition struct{}

func (stubCondition) String() string { return "stub_cond" }

type stubExpression struct{}

func (stubExpression) String() string { return "stub_expr" }

func TestQuery_ErrorAndEdgeBranches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := newQueryTestServer(t, countriesBlobs())
	defer srv.Close()

	baseQ := dal.From(dal.NewRootCollectionRef("countries", "")).NewQuery().
		SelectKeysOnly(reflect.String)

	// 1. collectionFromQuery errors: nil definition, nil From(), non-CollectionRef Base(), nil RecordFile.
	nilDefTx := readonlyTx{db: &githubDB{def: nil}}
	if _, err := nilDefTx.executeQueryToRecordsReader(ctx, baseQ); err == nil {
		t.Fatal("expected error for nil definition")
	}
	validDef := buildSingleRecordDef("countries", "data/countries", "{key}.yaml")
	validTx := readonlyTx{db: &githubDB{def: validDef, cfg: Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: srv.URL + "/"}}}
	if _, err := validTx.executeQueryToRecordsReader(ctx, customFromStructuredQuery{StructuredQuery: baseQ, from: nil}); err == nil {
		t.Fatal("expected error for nil FROM")
	}
	if _, err := validTx.executeQueryToRecordsReader(ctx, customFromStructuredQuery{StructuredQuery: baseQ, from: nilBaseFromSource{}}); err == nil {
		t.Fatal("expected error for non-CollectionRef Base()")
	}
	noRecFileDef := buildSingleRecordDef("countries", "data/countries", "{key}.yaml")
	noRecFileDef.Collections["countries"].RecordFile = nil
	noRecFileTx := readonlyTx{db: &githubDB{def: noRecFileDef}}
	if _, err := noRecFileTx.executeQueryToRecordsReader(ctx, baseQ); err == nil {
		t.Fatal("expected error for collection with nil RecordFile")
	}

	// 2. scanCollectionRecords unsupported RecordType.
	badTypeDef := buildSingleRecordDef("countries", "data/countries", "{key}.yaml")
	badTypeDef.Collections["countries"].RecordFile.RecordType = "unsupported_type"
	badTypeTx := readonlyTx{db: &githubDB{def: badTypeDef}}
	if _, err := badTypeTx.executeQueryToRecordsReader(ctx, baseQ); err == nil {
		t.Fatal("expected error for unsupported RecordType")
	}

	// 3. scanSingleRecords errors: invalid Config for NewTreeWriter, closed server for ListFilesUnder.
	badCfgTx := readonlyTx{db: &githubDB{def: validDef, cfg: Config{}}}
	if _, err := badCfgTx.executeQueryToRecordsReader(ctx, baseQ); err == nil {
		t.Fatal("expected error for invalid Config in scanSingleRecords")
	}
	closedSrv := newQueryTestServer(t, nil)
	closedSrv.Close()
	closedCfg := Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: closedSrv.URL + "/"}
	closedTx := readonlyTx{db: &githubDB{def: validDef, cfg: closedCfg}}
	if _, err := closedTx.executeQueryToRecordsReader(ctx, baseQ); err == nil {
		t.Fatal("expected error for closed server in scanSingleRecords")
	}

	// 4. scanSingleRecords edge cases: IsExcluded, non-matching filename (recordKey==""),
	// missing blob on GetContents (!found), and readSingleRecord parse error.
	edgeBlobs := map[string]string{
		"data/countries/$records/ignore.yaml": "name: Ignored\n",
		"data/countries/$records/notes.txt":   "not a yaml record\n",
		"data/countries/$records/us.yaml":     "name: USA\n",
	}
	// Custom server where tree also lists a "vanished.yaml" that 404s on GET /contents/.
	edgeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/trees/tree-sha") {
			w.Header().Set("Content-Type", "application/json")
			writeJSON(w, map[string]any{
				"sha":       "tree-sha",
				"truncated": false,
				"tree": []map[string]any{
					{"path": "data/countries/$records/ignore.yaml", "mode": "100644", "type": "blob", "sha": "s1"},
					{"path": "data/countries/$records/notes.txt", "mode": "100644", "type": "blob", "sha": "s2"},
					{"path": "data/countries/$records/vanished.yaml", "mode": "100644", "type": "blob", "sha": "s3"},
					{"path": "data/countries/$records/us.yaml", "mode": "100644", "type": "blob", "sha": "s4"},
				},
			})
			return
		}
		(&queryMockServer{owner: "ingitdb", repo: "ingitdb-cli", branch: "main", blobs: edgeBlobs}).serve(w, r)
	}))
	defer edgeSrv.Close()

	edgeDef := buildSingleRecordDef("countries", "data/countries", "{key}.yaml")
	edgeDef.Collections["countries"].RecordFile.ExcludeRegex = `^ignore\.yaml$`
	edgeCfg := Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: edgeSrv.URL + "/"}
	edgeDB, err := NewGitHubDBWithDef(edgeCfg, edgeDef)
	if err != nil {
		t.Fatalf("NewGitHubDBWithDef: %v", err)
	}
	recs := runQuery(t, edgeDB, baseQ)
	if got := recordIDs(recs); !reflect.DeepEqual(got, []string{"us"}) {
		t.Fatalf("edge scan ids = %v, want [us]", got)
	}

	// readSingleRecord error in scanSingleRecords (invalid YAML).
	badYAMLBlobs := map[string]string{
		"data/countries/$records/us.yaml": ":::\n\t:",
	}
	badYAMLSrv := newQueryTestServer(t, badYAMLBlobs)
	defer badYAMLSrv.Close()
	badYAMLDB := newQueryTestDB(t, badYAMLSrv)
	if err := badYAMLDB.RunReadonlyTransaction(ctx, func(ctx context.Context, tx dal.ReadTransaction) error {
		_, err := tx.ExecuteQueryToRecordsReader(ctx, baseQ)
		return err
	}); err == nil {
		t.Fatal("expected error for invalid YAML in scanSingleRecords")
	}

	// buildKeyExtractor without {key} placeholder.
	noPlaceholderFn := buildKeyExtractor("singleton.yaml")
	if got := noPlaceholderFn("sub/singleton.yaml"); got != "singleton" {
		t.Errorf("buildKeyExtractor(singleton.yaml) = %q, want singleton", got)
	}

	// 5. applyWhere error propagation inside executeQueryToRecordsReader.
	badWhereQ := dal.From(dal.NewRootCollectionRef("countries", "")).NewQuery().
		Where(stubCondition{}).
		SelectKeysOnly(reflect.String)
	validDB := newQueryTestDB(t, srv)
	if err := validDB.RunReadonlyTransaction(ctx, func(ctx context.Context, tx dal.ReadTransaction) error {
		_, err := tx.ExecuteQueryToRecordsReader(ctx, badWhereQ)
		return err
	}); err == nil {
		t.Fatal("expected error for unsupported condition in WHERE")
	}
}

func TestQuery_ScanMapOfRecords(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mapDef := mapRecordDef("tags", "data/tags", "tags.json")
	recordPath := resolveRecordPath(mapDef.Collections["tags"], "")

	// 1. Success path.
	srv := newQueryTestServer(t, map[string]string{
		recordPath: `{"a": {"title": "Alpha"}, "b": {"title": "Beta"}}`,
	})
	defer srv.Close()
	cfg := Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: srv.URL + "/"}
	db, err := NewGitHubDBWithDef(cfg, mapDef)
	if err != nil {
		t.Fatalf("NewGitHubDBWithDef: %v", err)
	}
	q := dal.From(dal.NewRootCollectionRef("tags", "")).NewQuery().
		OrderBy(dal.AscendingField("$id")).
		SelectKeysOnly(reflect.String)
	if got := recordIDs(runQuery(t, db, q)); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("scanMapOfRecords ids = %v, want [a b]", got)
	}

	// 2. Missing map file (!found) -> empty result.
	emptySrv := newQueryTestServer(t, nil)
	defer emptySrv.Close()
	emptyDB, err := NewGitHubDBWithDef(Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: emptySrv.URL + "/"}, mapDef)
	if err != nil {
		t.Fatalf("NewGitHubDBWithDef: %v", err)
	}
	if got := runQuery(t, emptyDB, q); len(got) != 0 {
		t.Fatalf("missing map file: got %d records, want 0", len(got))
	}

	// 3. Invalid map content -> parse error.
	badMapSrv := newQueryTestServer(t, map[string]string{recordPath: `{invalid json`})
	defer badMapSrv.Close()
	badMapDB, err := NewGitHubDBWithDef(Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: badMapSrv.URL + "/"}, mapDef)
	if err != nil {
		t.Fatalf("NewGitHubDBWithDef: %v", err)
	}
	if err := badMapDB.RunReadonlyTransaction(ctx, func(ctx context.Context, tx dal.ReadTransaction) error {
		_, err := tx.ExecuteQueryToRecordsReader(ctx, q)
		return err
	}); err == nil {
		t.Fatal("expected parse error for invalid map JSON")
	}

	// 4. ReadFile error on closed server.
	closedSrv := newQueryTestServer(t, nil)
	closedSrv.Close()
	closedDB, err := NewGitHubDBWithDef(Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: closedSrv.URL + "/"}, mapDef)
	if err != nil {
		t.Fatalf("NewGitHubDBWithDef: %v", err)
	}
	if err := closedDB.RunReadonlyTransaction(ctx, func(ctx context.Context, tx dal.ReadTransaction) error {
		_, err := tx.ExecuteQueryToRecordsReader(ctx, q)
		return err
	}); err == nil {
		t.Fatal("expected ReadFile error on closed server")
	}
}

func TestQuery_ConditionsOrderByAndHelpers(t *testing.T) {
	t.Parallel()
	data := map[string]any{"n": 10, "s": "b"}

	// GroupCondition: And match, And non-match, And error, unsupported operator.
	andMatch := dal.NewGroupCondition(dal.And,
		dal.WhereField("n", dal.GreaterThen, 5),
		dal.WhereField("n", dal.GreaterOrEqual, 10),
		dal.WhereField("n", dal.LessThen, 20),
		dal.WhereField("n", dal.LessOrEqual, 10),
		dal.WhereField("$id", dal.Equal, "k1"),
	)
	if ok, err := evaluateCondition(andMatch, data, "k1"); err != nil || !ok {
		t.Fatalf("andMatch: ok=%v err=%v", ok, err)
	}
	andNoMatch := dal.NewGroupCondition(dal.And,
		dal.WhereField("n", dal.Equal, 999),
	)
	if ok, err := evaluateCondition(andNoMatch, data, "k1"); err != nil || ok {
		t.Fatalf("andNoMatch: ok=%v err=%v", ok, err)
	}
	andErr := dal.NewGroupCondition(dal.And, stubCondition{})
	if _, err := evaluateCondition(andErr, data, "k1"); err == nil {
		t.Fatal("expected error from GroupCondition with invalid inner condition")
	}
	orCond := dal.NewGroupCondition(dal.Or, dal.WhereField("n", dal.Equal, 10))
	if _, err := evaluateCondition(orCond, data, "k1"); err == nil {
		t.Fatal("expected error for unsupported group operator Or")
	}

	// evaluateComparison: left error, right error, unsupported operator, In operator.
	if _, err := evaluateComparison(dal.Comparison{Left: stubExpression{}, Operator: dal.Equal, Right: dal.NewConstant(1)}, data, "k1"); err == nil {
		t.Fatal("expected error for unsupported left expression")
	}
	if _, err := evaluateComparison(dal.Comparison{Left: dal.Field("n"), Operator: dal.Equal, Right: stubExpression{}}, data, "k1"); err == nil {
		t.Fatal("expected error for unsupported right expression")
	}
	if _, err := evaluateComparison(dal.Comparison{Left: dal.Field("n"), Operator: "???", Right: dal.NewConstant(10)}, data, "k1"); err == nil {
		t.Fatal("expected error for unsupported comparison operator")
	}

	// In operator: dal.Array match, dal.Constant scalar match, nil value, unsupported right expression (returns nil candidates -> false).
	inArrayMatch := dal.Comparison{Left: dal.Field("n"), Operator: dal.In, Right: dal.NewArray([]int{5, 10, 15})}
	if ok, err := evaluateComparison(inArrayMatch, data, "k1"); err != nil || !ok {
		t.Fatalf("inArrayMatch: ok=%v err=%v", ok, err)
	}
	inConstSingle := dal.Comparison{Left: dal.Field("n"), Operator: dal.In, Right: dal.NewConstant(10)}
	if ok, err := evaluateComparison(inConstSingle, data, "k1"); err != nil || !ok {
		t.Fatalf("inConstSingle: ok=%v err=%v", ok, err)
	}
	inNil := dal.Comparison{Left: dal.Field("n"), Operator: dal.In, Right: dal.NewConstant(nil)}
	if ok, err := evaluateComparison(inNil, data, "k1"); err != nil || ok {
		t.Fatalf("inNil: ok=%v err=%v", ok, err)
	}
	inOtherExpr := dal.Comparison{Left: dal.Field("n"), Operator: dal.In, Right: stubExpression{}}
	if ok, err := evaluateComparison(inOtherExpr, data, "k1"); err != nil || ok {
		t.Fatalf("inOtherExpr: ok=%v err=%v", ok, err)
	}

	// applyOrderBy with non-FieldRef expression, tie-breaker (cmp == 0), and recordData with non-map Data().
	r1 := record.NewRecordWithData(record.NewKeyWithID("c", "b"), map[string]any{"tie": 1})
	r2 := record.NewRecordWithData(record.NewKeyWithID("c", "a"), map[string]any{"tie": 1})
	rNonMap := record.NewRecordWithData(record.NewKeyWithID("c", "c"), "not-a-map")
	recs := []record.Record{r1, r2, rNonMap}
	applyOrderBy(recs, []dal.OrderExpression{
		dal.Ascending(dal.NewConstant("ignored")),
		dal.AscendingField("tie"),
	})
	if got := recordData(rNonMap); len(got) != 0 {
		t.Errorf("recordData(non-map) = %v, want empty map", got)
	}

	// toFloat64 across all numeric types.
	numericInputs := []any{
		int(1), int8(2), int16(3), int32(4), int64(5),
		uint(6), uint8(7), uint16(8), uint32(9), uint64(10),
		float32(11.5), float64(12.5),
	}
	for _, v := range numericInputs {
		if _, ok := toFloat64(v); !ok {
			t.Errorf("toFloat64(%T) = false, want true", v)
		}
	}
	if _, ok := toFloat64("not-a-number"); ok {
		t.Error("toFloat64(string) = true, want false")
	}
}
