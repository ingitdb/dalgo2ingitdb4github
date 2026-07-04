package dalgo2ghingitdb

import (
	"fmt"
	"path"

	"github.com/dal-go/dalgo/dal"
	"github.com/ingitdb/ingitdb-go/ingitdb"
)

// resolveScopedCollection resolves the collection definition for a (possibly
// nested) collection identified by its leaf name and the key of its parent
// record, returning a copy of that definition whose DirPath is scoped to the
// concrete parent-record path in the repo.
//
// On-disk / in-repo layout (Option A — mirrors Firestore's document/subcollection
// nesting so a repo stays human-browsable): a subcollection's data lives *under*
// its parent record's directory, interleaving parent-record ids with
// subcollection names:
//
//	spaces/family/contacts/c1.yaml   (key: spaces/family/contacts/c1)
//	spaces/work/contacts/c1.yaml     (key: spaces/work/contacts/c1)
//
// This is what keeps records physically scoped by their parent chain: two keys
// that share a leaf collection + record id but differ in their parent chain no
// longer collide (previously both mapped to the same flat contacts path and
// clobbered each other).
//
// A top-level collection (parent == nil) resolves exactly as before: a flat
// lookup in def.Collections with the schema-declared DirPath untouched. Only
// nested keys take the scoping path, so existing (non-nested) behaviour is
// unchanged.
//
// The returned CollectionDef is a shallow copy of the schema definition with
// only DirPath overridden; RecordFile, Columns, format, etc. still come from the
// subcollection's own schema so encoding/decoding is unaffected. Paths use
// forward slashes (path.Join) because inGitDB repo paths are always POSIX.
func resolveScopedCollection(def *ingitdb.Definition, collection string, parent *dal.Key) (*ingitdb.CollectionDef, error) {
	if def == nil {
		return nil, fmt.Errorf("definition is required")
	}

	// Top-level collection: preserve the original flat behaviour verbatim,
	// including the exact "collection %q not found in definition" message that
	// callers (and tests) rely on.
	if parent == nil {
		colDef, ok := def.Collections[collection]
		if !ok {
			return nil, fmt.Errorf("collection %q not found in definition", collection)
		}
		return colDef, nil
	}

	// Build the ancestor record chain from root down to the immediate parent.
	// Each step is a (collection, recordID) pair describing one ancestor record.
	type step struct {
		col string
		id  string
	}
	var ancestors []step
	for k := parent; k != nil; k = k.Parent() {
		ancestors = append([]step{{col: k.Collection(), id: fmt.Sprintf("%v", k.ID)}}, ancestors...)
	}

	// Descend from the root collection, interleaving parent-record ids and
	// subcollection names into the in-repo path.
	rootCol := ancestors[0].col
	cur, ok := def.Collections[rootCol]
	if !ok {
		return nil, fmt.Errorf("collection %q not found in definition", rootCol)
	}
	dir := cur.DirPath
	// Walk the intermediate ancestors (root's children, grandchildren, ...).
	for i := 1; i < len(ancestors); i++ {
		parentID := ancestors[i-1].id
		subColID := ancestors[i].col
		sub, ok := cur.SubCollections[subColID]
		if !ok {
			return nil, fmt.Errorf("subcollection %q under %q not found in definition", subColID, cur.ID)
		}
		dir = path.Join(dir, parentID, subColID)
		cur = sub
	}

	// Finally descend into the target collection as a subcollection of the
	// deepest ancestor record.
	parentID := ancestors[len(ancestors)-1].id
	target, ok := cur.SubCollections[collection]
	if !ok {
		return nil, fmt.Errorf("subcollection %q under %q not found in definition", collection, cur.ID)
	}
	dir = path.Join(dir, parentID, collection)

	scoped := *target
	scoped.DirPath = dir
	return &scoped, nil
}
