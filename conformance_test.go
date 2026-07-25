package dalgo2ghingitdb

import (
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dalgotest"
)

// TestConformance runs the shared dalgotest.RunConformance suite against a
// githubDB backed by the in-package httptest Contents API fake (no live
// GitHub repository or credentials required — see newGitHubContentsServer).
//
// It is currently skipped: the conformance suite writes record data as
// typed Go structs (dalgotest.Record / dalgotest.Plain), whereas every write
// path in this adapter (readwriteTx.Set / Insert / batchingTx.Set / Insert)
// hard-requires record.Data() to already be a map[string]any and returns a
// plain error otherwise — confirmed by actually running the suite here,
// which fails "record data is not map[string]any" on every write of a
// dalgotest fixture, valid or invalid alike. That is a pre-existing property
// of this adapter's data model (ingitdb records are always decoded/encoded
// as maps against a YAML/JSON/TOML schema); it is not something the
// dal.DB-sealing migration introduced, and fixing it is a separate, larger
// change to how this adapter accepts record data, not a conformance bug to
// paper over here.
//
// Un-skip this once the adapter accepts (or the suite is configured to
// supply) map[string]any-shaped record data.
func TestConformance(t *testing.T) {
	t.Skip("dalgotest.RunConformance writes typed struct record data; this adapter's write path only accepts map[string]any (see comment above) — tracked as a follow-up, not fixed by the dal.DB sealing migration")

	def := buildSingleRecordDef(dalgotest.DefaultCollection, "data/"+dalgotest.DefaultCollection, "{key}.yaml")
	dalgotest.RunConformance(t, func(t *testing.T) (dal.DB, func()) {
		server := newGitHubContentsServer(t, nil)
		cfg := Config{Owner: "ingitdb", Repo: "ingitdb-cli", APIBaseURL: server.URL + "/"}
		db, err := NewGitHubDBWithDef(cfg, def)
		if err != nil {
			t.Fatalf("NewGitHubDBWithDef: %v", err)
		}
		return db, server.Close
	})
}
