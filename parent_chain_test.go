package dalgo2ghingitdb

// parent_chain_test.go is the regression suite for the per-parent ("per-space")
// scoping fix. Before the fix the adapter mapped a dal.Key to an in-repo path
// using only the leaf collection + record id, dropping the parent chain, so two
// keys that share a leaf (e.g. contacts/c1) but live under different parents
// (spaces/family vs spaces/work) collided on the same file and clobbered each
// other. After the fix each nested record is scoped under its parent record's
// directory (spaces/<spaceID>/contacts/...), so they no longer collide.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"

	"github.com/ingitdb/ingitdb-go/ingitdb"
)

// newStatefulContentsServer returns an httptest server that emulates the GitHub
// contents API with real read-after-write semantics: a PUT stores the decoded
// content keyed by its repo path, and a subsequent GET of that path serves it
// back (404 until first written). Unlike newGitHubContentsServer it does not
// gate GET on a pre-seeded fixture, so it can round-trip freshly written paths —
// which is exactly what the per-parent scoping regression needs.
func newStatefulContentsServer(t *testing.T) *httptest.Server {
	t.Helper()
	contents := make(map[string][]byte)
	const pathPrefix = "/api/v3/repos/ingitdb/ingitdb-cli/contents/"
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, pathPrefix) {
			http.NotFound(w, r)
			return
		}
		repoPath := strings.TrimPrefix(r.URL.Path, pathPrefix)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPut:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			decoded, err := base64.StdEncoding.DecodeString(body["content"].(string))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			contents[repoPath] = decoded
			_ = json.NewEncoder(w).Encode(map[string]any{
				"content": map[string]any{"name": path.Base(repoPath), "path": repoPath, "sha": "sha-" + repoPath},
			})
		case http.MethodGet:
			content, ok := contents[repoPath]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type": "file", "encoding": "base64",
				"content": base64.StdEncoding.EncodeToString(content),
				"sha":     "sha-" + repoPath, "name": path.Base(repoPath), "path": repoPath,
			})
		default:
			http.Error(w, "unexpected method "+r.Method, http.StatusInternalServerError)
		}
	}))
}

// spacesWithContactsSubDef returns a definition with a top-level "spaces"
// collection and a "contacts" subcollection nested under it. Both are
// single-record YAML collections with a {key}.yaml record file.
func spacesWithContactsSubDef() *ingitdb.Definition {
	contacts := &ingitdb.CollectionDef{
		ID:      "contacts",
		DirPath: "spaces/contacts", // schema-declared; scoping overrides this per parent record
		RecordFile: &ingitdb.RecordFileDef{
			Name:       "{key}.yaml",
			Format:     "yaml",
			RecordType: ingitdb.SingleRecord,
		},
		Columns: map[string]*ingitdb.ColumnDef{
			"name": {Type: ingitdb.ColumnTypeString},
		},
	}
	spaces := &ingitdb.CollectionDef{
		ID:      "spaces",
		DirPath: "spaces",
		RecordFile: &ingitdb.RecordFileDef{
			Name:       "{key}.yaml",
			Format:     "yaml",
			RecordType: ingitdb.SingleRecord,
		},
		Columns: map[string]*ingitdb.ColumnDef{
			"name": {Type: ingitdb.ColumnTypeString},
		},
		SubCollections: map[string]*ingitdb.CollectionDef{
			"contacts": contacts,
		},
	}
	return &ingitdb.Definition{
		Collections: map[string]*ingitdb.CollectionDef{
			"spaces": spaces,
		},
	}
}

// TestResolveScopedCollection_DistinctPathsForSameLeaf proves the core fix at
// the path-mapping layer: two keys that share a leaf collection + record id but
// differ in their parent chain resolve to DISTINCT in-repo paths. It also
// asserts that a top-level (non-nested) key still resolves to the flat,
// schema-declared path so existing behaviour is unchanged.
func TestResolveScopedCollection_DistinctPathsForSameLeaf(t *testing.T) {
	t.Parallel()
	def := spacesWithContactsSubDef()

	familyKey := dal.NewKeyWithParentAndID(dal.NewKeyWithID("spaces", "family"), "contacts", "c1")
	workKey := dal.NewKeyWithParentAndID(dal.NewKeyWithID("spaces", "work"), "contacts", "c1")

	familyDef, err := resolveScopedCollection(def, familyKey.Collection(), familyKey.Parent())
	if err != nil {
		t.Fatalf("resolveScopedCollection(family): %v", err)
	}
	workDef, err := resolveScopedCollection(def, workKey.Collection(), workKey.Parent())
	if err != nil {
		t.Fatalf("resolveScopedCollection(work): %v", err)
	}

	familyPath := resolveRecordPath(familyDef, "c1")
	workPath := resolveRecordPath(workDef, "c1")

	wantFamily := "spaces/family/contacts/$records/c1.yaml"
	wantWork := "spaces/work/contacts/$records/c1.yaml"
	if familyPath != wantFamily {
		t.Errorf("family path = %q, want %q", familyPath, wantFamily)
	}
	if workPath != wantWork {
		t.Errorf("work path = %q, want %q", workPath, wantWork)
	}
	if familyPath == workPath {
		t.Fatalf("regression: same-leaf keys under different parents collide on %q", familyPath)
	}

	// Top-level key is untouched: flat schema-declared DirPath.
	topKey := dal.NewKeyWithID("spaces", "family")
	topDef, err := resolveScopedCollection(def, topKey.Collection(), topKey.Parent())
	if err != nil {
		t.Fatalf("resolveScopedCollection(top-level): %v", err)
	}
	if got, want := resolveRecordPath(topDef, "family"), "spaces/$records/family.yaml"; got != want {
		t.Errorf("top-level path = %q, want %q", got, want)
	}
}

// TestResolveScopedCollection_DeepNesting exercises the intermediate-ancestor
// loop: a grandchild collection (spaces/{s}/projects/{p}/tasks/{t}) must
// interleave every parent-record id and subcollection name into the path.
func TestResolveScopedCollection_DeepNesting(t *testing.T) {
	t.Parallel()
	tasks := &ingitdb.CollectionDef{
		ID: "tasks", DirPath: "ignored",
		RecordFile: &ingitdb.RecordFileDef{Name: "{key}.yaml", Format: "yaml", RecordType: ingitdb.SingleRecord},
	}
	projects := &ingitdb.CollectionDef{
		ID: "projects", DirPath: "ignored",
		RecordFile:     &ingitdb.RecordFileDef{Name: "{key}.yaml", Format: "yaml", RecordType: ingitdb.SingleRecord},
		SubCollections: map[string]*ingitdb.CollectionDef{"tasks": tasks},
	}
	spaces := &ingitdb.CollectionDef{
		ID: "spaces", DirPath: "spaces",
		RecordFile:     &ingitdb.RecordFileDef{Name: "{key}.yaml", Format: "yaml", RecordType: ingitdb.SingleRecord},
		SubCollections: map[string]*ingitdb.CollectionDef{"projects": projects},
	}
	def := &ingitdb.Definition{Collections: map[string]*ingitdb.CollectionDef{"spaces": spaces}}

	// key: spaces/s1/projects/p1/tasks/t1
	parent := dal.NewKeyWithParentAndID(dal.NewKeyWithParentAndID(dal.NewKeyWithID("spaces", "s1"), "projects", "p1"), "tasks", "t1").Parent()
	colDef, err := resolveScopedCollection(def, "tasks", parent)
	if err != nil {
		t.Fatalf("resolveScopedCollection: %v", err)
	}
	if got, want := resolveRecordPath(colDef, "t1"), "spaces/s1/projects/p1/tasks/$records/t1.yaml"; got != want {
		t.Errorf("deep nested path = %q, want %q", got, want)
	}
}

// TestResolveScopedCollection_Errors covers the error branches: nil definition,
// unknown root collection, and an unknown subcollection under a known parent.
func TestResolveScopedCollection_Errors(t *testing.T) {
	t.Parallel()
	def := spacesWithContactsSubDef()

	if _, err := resolveScopedCollection(nil, "contacts", dal.NewKeyWithID("spaces", "x")); err == nil {
		t.Error("nil definition: expected error, got nil")
	}

	// Unknown root collection with a parent.
	if _, err := resolveScopedCollection(def, "contacts", dal.NewKeyWithID("unknown", "x")); err == nil ||
		!strings.Contains(err.Error(), "not found in definition") {
		t.Errorf("unknown root: got %v, want 'not found in definition'", err)
	}

	// Unknown subcollection under a known parent record.
	if _, err := resolveScopedCollection(def, "widgets", dal.NewKeyWithID("spaces", "family")); err == nil ||
		!strings.Contains(err.Error(), "not found in definition") {
		t.Errorf("unknown subcollection: got %v, want 'not found in definition'", err)
	}
}

// TestGitHubDB_NestedKeys_ScopedByParentRecord is the end-to-end round-trip
// regression: write a contact "c1" under spaces/family and a different contact
// "c1" under spaces/work through the real Set path, then read both back. Before
// the fix the second write clobbered the first (both mapped to
// contacts/$records/c1.yaml) so both reads would return "Bob"; after the fix
// each is physically scoped under its parent space and round-trips independently.
func TestGitHubDB_NestedKeys_ScopedByParentRecord(t *testing.T) {
	t.Parallel()
	// Stateful fake: PUTs are stored and served back on GET, so a write→read
	// round-trip works and a collision would be observable as a clobbered value.
	server := newStatefulContentsServer(t)
	defer server.Close()

	cfg := Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: server.URL + "/"}
	db, err := NewGitHubDBWithDef(cfg, spacesWithContactsSubDef())
	if err != nil {
		t.Fatalf("NewGitHubDBWithDef: %v", err)
	}

	familyContact := dal.NewKeyWithParentAndID(dal.NewKeyWithID("spaces", "family"), "contacts", "c1")
	workContact := dal.NewKeyWithParentAndID(dal.NewKeyWithID("spaces", "work"), "contacts", "c1")

	ctx := context.Background()
	if err := db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		if setErr := tx.Set(ctx, dal.NewRecordWithData(familyContact, map[string]any{"name": "Alice"})); setErr != nil {
			return setErr
		}
		return tx.Set(ctx, dal.NewRecordWithData(workContact, map[string]any{"name": "Bob"}))
	}); err != nil {
		t.Fatalf("write nested contacts: %v", err)
	}

	famRec := dal.NewRecordWithData(familyContact, map[string]any{})
	workRec := dal.NewRecordWithData(workContact, map[string]any{})
	if getErr := db.Get(ctx, famRec); getErr != nil {
		t.Fatalf("get family contact: %v", getErr)
	}
	if getErr := db.Get(ctx, workRec); getErr != nil {
		t.Fatalf("get work contact: %v", getErr)
	}
	if got := famRec.Data().(map[string]any)["name"]; got != "Alice" {
		t.Errorf("family contact name: got %v, want Alice (records collided across parents?)", got)
	}
	if got := workRec.Data().(map[string]any)["name"]; got != "Bob" {
		t.Errorf("work contact name: got %v, want Bob", got)
	}
}
