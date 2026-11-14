package dalgo2ghingitdb

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/dal-go/dalgo/dal"
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
// "." (where this driver wrote before) is mislaid, at any level of the parent
// chain. Every write, and a read that finds nothing at the defined place (a Get
// of a missing record, a query with no rows), checks there and fails with
// ErrMislaidSubCollection (see resolveScopedInstance and failIfMislaid),
// instead of returning nothing or writing a second copy.
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

// mislaidCheck names a place earlier versions of this driver put a
// subcollection instance (dir), at one level of the parent chain, and the
// place the definition gives it (expected).
type mislaidCheck struct {
	dir, expected, parentCol, sub, parentID, base string
}

func (m *mislaidCheck) err(entries int) error {
	return fmt.Errorf("%w: subcollection %s/%s of parent record %q: %s holds %d entr(ies), but the definition puts them at %s (%s keeps its records in record_file.records_dir %q). Move them there, or set record_file.records_dir: \".\" on %s and move its own records out of its records directory",
		ErrMislaidSubCollection, m.parentCol, m.sub, m.parentID, m.dir, entries, m.expected, m.parentCol, m.base, m.parentCol)
}

// recordsBasePath is the directory, below a collection's DirPath, that holds
// its record files and its records' subcollection directories.
func recordsBasePath(colDef *ingitdb.CollectionDef) string {
	if colDef.RecordFile == nil {
		return ""
	}
	return colDef.RecordFile.RecordsBasePath()
}

// ErrParentKeyEscapes reports a parent record ID (such as "..") that would
// place a subcollection outside the root collection's directory.
var ErrParentKeyEscapes = errors.New("parent record ID leads outside the collection directory")

// refuseKeyEncoding fails with dal.ErrNotSupported for a collection that
// declares record_file.key_encoding: this driver does not encode or decode
// record keys yet, and reading or writing such a collection with raw keys
// would show file names as keys and write unsafe names.
func refuseKeyEncoding(col *ingitdb.CollectionDef) error {
	if col.RecordFile != nil && col.RecordFile.EncodesKeys() {
		return fmt.Errorf("%w: collection %q declares record_file.key_encoding %q, which %s does not read or write yet; use the local driver",
			dal.ErrNotSupported, col.ID, col.RecordFile.KeyEncoding, DatabaseID)
	}
	return nil
}

// recordsDirHead is the first path segment of a records directory.
func recordsDirHead(base string) string {
	head, _, _ := strings.Cut(base, "/")
	return head
}

// within reports whether the repository path p is root or below it; an empty
// root is the repository root.
func within(root, p string) bool {
	if root == "" || root == "." {
		return p != ".." && !strings.HasPrefix(p, "../")
	}
	return p == root || strings.HasPrefix(p, root+"/")
}

// levelCheck is the mislaid-data check for the instance of sub under parent
// record parentID of collection cur, whose defined directory is parentDir; nil
// when cur keeps its records in its own directory (no other layout) or when
// parentID is the first segment of cur's records directory (the path is
// inside that directory).
func levelCheck(parentDir string, cur *ingitdb.CollectionDef, parentID, sub string) *mislaidCheck {
	base := recordsBasePath(cur)
	if base == "" || parentID == recordsDirHead(base) {
		return nil
	}
	return &mislaidCheck{dir: path.Join(parentDir, parentID, sub), expected: path.Join(parentDir, base, parentID, sub),
		parentCol: cur.ID, sub: sub, parentID: parentID, base: base}
}

// resolveScopedInstance is resolveScopedCollection that also returns, for each
// level of a nested collection's parent chain where an earlier location
// differs, where to look for mislaid data.
func resolveScopedInstance(def *ingitdb.Definition, collection string, parent *record.Key) (*ingitdb.CollectionDef, []*mislaidCheck, error) {
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
		if err := refuseKeyEncoding(colDef); err != nil {
			return nil, nil, err
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
	rootDir := cur.DirPath
	dir := rootDir
	var checks []*mislaidCheck
	addCheck := func(c *mislaidCheck) error {
		if c == nil {
			return nil
		}
		if !within(rootDir, c.dir) {
			return fmt.Errorf("%w: %s", ErrParentKeyEscapes, c.dir)
		}
		checks = append(checks, c)
		return nil
	}
	// Walk the intermediate ancestors (root's children, grandchildren, ...).
	for i := 1; i < len(ancestors); i++ {
		parentID := ancestors[i-1].id
		subColID := ancestors[i].col
		sub, ok := cur.SubCollections[subColID]
		if !ok {
			return nil, nil, fmt.Errorf("%w: subcollection %q under %q not found in definition", errCollectionNotInDefinition, subColID, cur.ID)
		}
		if err := refuseKeyEncoding(cur); err != nil {
			return nil, nil, err
		}
		if err := addCheck(levelCheck(dir, cur, parentID, subColID)); err != nil {
			return nil, nil, err
		}
		dir = path.Join(dir, recordsBasePath(cur), parentID, subColID)
		cur = sub
	}

	// Finally descend into the target collection as a subcollection of the
	// deepest ancestor record.
	parentID := ancestors[len(ancestors)-1].id
	target, ok := cur.SubCollections[collection]
	if !ok {
		return nil, nil, fmt.Errorf("%w: subcollection %q under %q not found in definition", errCollectionNotInDefinition, collection, cur.ID)
	}
	if err := refuseKeyEncoding(cur); err != nil {
		return nil, nil, err
	}
	if err := refuseKeyEncoding(target); err != nil {
		return nil, nil, err
	}
	if err := addCheck(levelCheck(dir, cur, parentID, collection)); err != nil {
		return nil, nil, err
	}
	dir = path.Join(dir, recordsBasePath(cur), parentID, collection)
	if !within(rootDir, dir) {
		return nil, nil, fmt.Errorf("%w: %s", ErrParentKeyEscapes, dir)
	}

	scoped := *target
	scoped.DirPath = dir
	return &scoped, checks, nil
}
