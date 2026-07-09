package dalgo2ghingitdb

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/google/go-github/v88/github"
)

// TestTreeReader_ReadsBlobFromHeadTree verifies that the tree-based reader
// resolves branch head → recursive tree → blob and returns the blob content,
// and reports found=false for a path absent from the head tree.
func TestTreeReader_ReadsBlobFromHeadTree(t *testing.T) {
	t.Parallel()
	fixtures := map[string]string{
		"data/tags/$records/active.yaml": "title: Active\n",
	}
	srv := makeBatchServer(t, fixtures)
	defer srv.Close()

	cfg := Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: srv.URL + "/"}
	tr, err := newTreeReader(cfg)
	if err != nil {
		t.Fatalf("newTreeReader: %v", err)
	}
	ctx := context.Background()

	content, found, err := tr.readFile(ctx, "data/tags/$records/active.yaml")
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}
	if !found {
		t.Fatal("expected blob to be found in head tree")
	}
	if string(content) != "title: Active\n" {
		t.Errorf("content = %q, want %q", content, "title: Active\n")
	}

	_, found, err = tr.readFile(ctx, "data/tags/$records/missing.yaml")
	if err != nil {
		t.Fatalf("readFile(missing): %v", err)
	}
	if found {
		t.Error("expected missing path to report found=false")
	}
}

// TestTreeReader_TruncatedTreeIsError verifies that a truncated tree is a hard
// error (consistent reads require a complete view of the head tree).
func TestTreeReader_TruncatedTreeIsError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		switch {
		case strings.Contains(p, "/git/ref/heads/main"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ref":    "refs/heads/main",
				"object": map[string]any{"sha": "c", "type": "commit"},
			})
		case strings.Contains(p, "/git/commits/c"):
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": "c", "tree": map[string]any{"sha": "t"}})
		case strings.Contains(p, "/git/trees/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": "t", "tree": []any{}, "truncated": true})
		default:
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	cfg := Config{Owner: "o", Repo: "r", Ref: "main", APIBaseURL: srv.URL + "/"}
	tr, err := newTreeReader(cfg)
	if err != nil {
		t.Fatalf("newTreeReader: %v", err)
	}
	if _, _, err := tr.readFile(context.Background(), "x.yaml"); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("expected truncated-tree error, got %v", err)
	}
}

// TestBatchingGitHubDB_Get_ReadAfterWrite verifies that the batching DB's Get
// reads through the Git Data API and returns a record whose blob is present in
// the head tree — the strongly-consistent read path.
func TestBatchingGitHubDB_Get_ReadAfterWrite(t *testing.T) {
	t.Parallel()
	def := singleRecordDef("tags", "data/tags", "{key}.yaml")
	fixtures := map[string]string{
		resolveRecordPath(def.Collections["tags"], "active"): "title: Active\n",
	}
	srv := makeBatchServer(t, fixtures)
	defer srv.Close()

	cfg := Config{Owner: "ingitdb", Repo: "ingitdb-cli", Ref: "main", APIBaseURL: srv.URL + "/"}
	bdb, err := NewBatchingGitHubDB(cfg, def, "test: read")
	if err != nil {
		t.Fatalf("NewBatchingGitHubDB: %v", err)
	}

	rec := dal.NewRecordWithData(dal.NewKeyWithID("tags", "active"), map[string]any{})
	if getErr := bdb.Get(context.Background(), rec); getErr != nil {
		t.Fatalf("Get: %v", getErr)
	}
	if !rec.Exists() {
		t.Fatal("expected record present in head tree to exist")
	}
	data := rec.Data().(map[string]any)
	if data["title"] != "Active" {
		t.Errorf("title = %v, want Active", data["title"])
	}
}

// TestDecodeBlobContent_Encodings covers base64 (with embedded newlines),
// utf-8, an unsupported encoding, and a nil blob.
func TestDecodeBlobContent_Encodings(t *testing.T) {
	t.Parallel()

	// base64 payload split across lines, as the GitHub blob API returns it.
	b64 := base64.StdEncoding.EncodeToString([]byte("hello world"))
	withNewlines := b64[:4] + "\n" + b64[4:]
	enc64 := "base64"
	got, err := decodeBlobContent(&github.Blob{Content: &withNewlines, Encoding: &enc64})
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("base64 decode = %q, want %q", got, "hello world")
	}

	utf := "utf-8"
	raw := "plain text"
	got, err = decodeBlobContent(&github.Blob{Content: &raw, Encoding: &utf})
	if err != nil {
		t.Fatalf("utf-8 decode: %v", err)
	}
	if string(got) != "plain text" {
		t.Errorf("utf-8 decode = %q, want %q", got, "plain text")
	}

	bad := "rot13"
	if _, err := decodeBlobContent(&github.Blob{Content: &raw, Encoding: &bad}); err == nil {
		t.Error("expected error for unsupported encoding")
	}

	if _, err := decodeBlobContent(nil); err == nil {
		t.Error("expected error for nil blob")
	}
}
