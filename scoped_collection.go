package dalgo2ghingitdb

import (
	"errors"
	"fmt"
	"path"

	"github.com/dal-go/record"
	"github.com/ingitdb/ingitdb-go/ingitdb"
)

// errCollectionNotInDefinition is returned (wrapped) by resolveScopedCollection
// when the key references a collection (or nested subcollection) absent from the
// loaded definition. Read paths (Get / GetMulti / queries) treat it as a
// record-not-found so that getting a record from an unknown collection behaves
// like getting a missing record, per the dalgo contract, rather than surfacing a
// hard error. Callers detect it with errors.Is.
var errCollectionNotInDefinition = errors.New("collection not found in definition")

// resolveScopedCollection resolves the collection definition for a (possibly
// nested) collection identified by its leaf name and the key of its parent
// record, returning a copy of that definition whose DirPath is scoped to the
// concrete parent-record path in the repo.
//
// In-repo layout: the one the definition defines (ingitdb-go
// subcollection-record-validation#req:subcollection-storage-convention), which
// `ingitdb validate` reads. A subcollection's data lives in a directory named
// by its parent record's key, next to the parent's record files, i.e. below
// the parent's records directory (record_file.records_dir; "$records" by
// default for a {key} name, "." for the data directory itself), interleaving
// parent-record ids with subcollection names:
//
//	spaces/$records/family/contacts/...   (default records_dir; key spaces/family/contacts/c1)
//	spaces/family/contacts/...            (records_dir: ".", the Firestore-mirror layout)
//
// Data at <parent DirPath>/<key>/<sub>/ while the parent's records_dir is not
// "." (where this driver wrote before) is mislaid; a query that finds the
// defined instance empty checks there and fails with ErrMislaidSubCollection
// (see resolveScopedInstance), instead of returning no rows.
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
func resolveScopedCollection(def *ingitdb.Definition, collection string, parent *record.Key) (*ingitdb.CollectionDef, error) {
	colDef, _, err := resolveScopedInstance(def, collection, parent)
	return colDef, err
}

// ErrMislaidSubCollection reports subcollection records found where the
// definition does not put them.
var ErrMislaidSubCollection = errors.New("subcollection records outside the location the definition defines")

// mislaidCheck names the place earlier versions of this driver put a
// subcollection instance, when it differs from the place the definition gives.
type mislaidCheck struct {
	dir, expected, parentCol, sub, parentID, base string
}

func (m *mislaidCheck) err(files int) error {
	return fmt.Errorf("%w: subcollection %s/%s of parent record %q: %s holds %d file(s), but the definition puts them at %s (%s keeps its records in record_file.records_dir %q). Move them there, or set record_file.records_dir: \".\" on %s and move its own records out of its records directory",
		ErrMislaidSubCollection, m.parentCol, m.sub, m.parentID, m.dir, files, m.expected, m.parentCol, m.base, m.parentCol)
}

// recordsBasePath is the directory, below a collection's DirPath, that holds
// its record files and its records' subcollection directories.
func recordsBasePath(colDef *ingitdb.CollectionDef) string {
	if colDef.RecordFile == nil {
		return ""
	}
	return colDef.RecordFile.RecordsBasePath()
}

// resolveScopedInstance is resolveScopedCollection that also returns, for a
// nested collection whose old location differs, where to look for mislaid
// data (nil otherwise).
func resolveScopedInstance(def *ingitdb.Definition, collection string, parent *record.Key) (*ingitdb.CollectionDef, *mislaidCheck, error) {
	if def == nil {
		return nil, nil, fmt.Errorf("definition is required")
	}

	// Top-level collection: preserve the original flat behaviour verbatim,
	// including the exact "collection %q not found in definition" message that
	// callers (and tests) rely on.
	if parent == nil {
		colDef, ok := def.Collections[collection]
		if !ok {
			return nil, nil, fmt.Errorf("%w: collection %q not found in definition", errCollectionNotInDefinition, collection)
		}
		return colDef, nil, nil
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
		return nil, nil, fmt.Errorf("%w: collection %q not found in definition", errCollectionNotInDefinition, rootCol)
	}
	dir := cur.DirPath
	legacy := dir // where earlier driver versions put the same instance
	// Walk the intermediate ancestors (root's children, grandchildren, ...).
	for i := 1; i < len(ancestors); i++ {
		parentID := ancestors[i-1].id
		subColID := ancestors[i].col
		sub, ok := cur.SubCollections[subColID]
		if !ok {
			return nil, nil, fmt.Errorf("%w: subcollection %q under %q not found in definition", errCollectionNotInDefinition, subColID, cur.ID)
		}
		dir = path.Join(dir, recordsBasePath(cur), parentID, subColID)
		legacy = path.Join(legacy, parentID, subColID)
		cur = sub
	}

	// Finally descend into the target collection as a subcollection of the
	// deepest ancestor record.
	parentID := ancestors[len(ancestors)-1].id
	target, ok := cur.SubCollections[collection]
	if !ok {
		return nil, nil, fmt.Errorf("%w: subcollection %q under %q not found in definition", errCollectionNotInDefinition, collection, cur.ID)
	}
	base := recordsBasePath(cur)
	dir = path.Join(dir, base, parentID, collection)
	legacy = path.Join(legacy, parentID, collection)

	scoped := *target
	scoped.DirPath = dir
	var check *mislaidCheck
	if legacy != dir {
		check = &mislaidCheck{dir: legacy, expected: dir, parentCol: cur.ID, sub: collection, parentID: parentID, base: base}
	}
	return &scoped, check, nil
}
