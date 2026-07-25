package dalgo2ghingitdb

import (
	"context"
	"errors"
	"fmt"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record/update"

	dalrecord "github.com/dal-go/record"
	"github.com/ingitdb/ingitdb-go/ingitdb"
)

// BatchingGitHubDB wraps a githubDB so that RunReadwriteTransaction buffers
// every tx.Set / tx.Insert / tx.Delete call inside the worker, then emits
// exactly one commit via the Git Data API when the worker returns nil.
//
// Reads (Get, RunReadonlyTransaction, ExecuteQuery*) delegate to the
// underlying githubDB unchanged — they read the pre-tx state from remote and
// do not observe buffered writes (set-mode callers fetch matches in a
// separate read-only pass before opening the write tx, so this limitation
// does not affect them).
//
// Use this in place of the per-file github DB when an operation may touch
// multiple records — `update --from --where`, `delete --from --where`,
// `update --from --all`, `delete --from --all`. Single-record operations
// already produce one commit through the Contents API and should keep
// using the plain githubDB.
type BatchingGitHubDB struct {
	*githubDB
	commitMessage string
	writer        *TreeWriter
}

// NewBatchingGitHubDB builds a BatchingGitHubDB for the given Config + def.
// commitMessage is the message used when flushing buffered changes; callers
// supply something human-readable like "ingitdb: update countries (batch)".
func NewBatchingGitHubDB(cfg Config, def *ingitdb.Definition, commitMessage string) (*BatchingGitHubDB, error) {
	return newBatchingGitHubDB(cfg, def, commitMessage, NewTreeWriter)
}

// newBatchingGitHubDB is the internal constructor; treeWriterFn defaults to
// NewTreeWriter in production and can be replaced in tests to inject failures.
func newBatchingGitHubDB(cfg Config, def *ingitdb.Definition, commitMessage string, treeWriterFn func(Config) (*TreeWriter, error)) (*BatchingGitHubDB, error) {
	if def == nil {
		return nil, fmt.Errorf("definition is required")
	}
	if commitMessage == "" {
		return nil, fmt.Errorf("commit message is required")
	}
	// The batching variant reads through the Git Data API (tree/blob) for
	// read-after-write consistency: it commits via the tree API and the
	// conformance suite does tight write-then-read, which the eventually
	// consistent Contents API cannot satisfy.
	reader, err := newConsistentGitHubFileReader(cfg)
	if err != nil {
		return nil, err
	}
	inner := &githubDB{
		cfg:        cfg,
		def:        def,
		fileReader: reader,
	}
	writer, err := treeWriterFn(cfg)
	if err != nil {
		return nil, err
	}
	return &BatchingGitHubDB{
		githubDB:      inner,
		commitMessage: commitMessage,
		writer:        writer,
	}, nil
}

// RunReadwriteTransaction overrides githubDB.RunReadwriteTransaction with a
// batching variant. Every Set / Insert / Delete inside f is buffered; when f
// returns nil, all buffered changes are flushed to GitHub as one commit. If
// f returns an error, no changes are committed (the remote is untouched).
func (db *BatchingGitHubDB) RunReadwriteTransaction(ctx context.Context, f dal.RWTxWorker, options ...dal.TransactionOption) error {
	opts := dal.NewTransactionOptions(options...)
	tx := &batchingTx{
		readonlyTx:    readonlyTx{db: db.githubDB},
		opts:          opts,
		bufferedFiles: make(map[string]TreeChange),
		workingMaps:   make(map[string]map[string]map[string]any),
		mapColDefs:    make(map[string]*ingitdb.CollectionDef),
		mapLoaded:     make(map[string]bool),
	}
	if err := f(ctx, tx); err != nil {
		return err
	}
	changes, err := tx.flushChanges()
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		return nil // nothing was buffered; no commit needed
	}
	// A transaction message (set via dal.TxWithMessage at start or
	// tx.Options().SetMessage during execution) overrides the construction-time
	// default commit message.
	commitMessage := db.commitMessage
	if msg := opts.Message(); msg != "" {
		commitMessage = msg
	}
	newSHA, err := db.writer.CommitChanges(ctx, commitMessage, changes)
	if err != nil {
		return err
	}
	// Record the new head so subsequent reads resolve the tree from this exact
	// commit (by SHA, immediately consistent) rather than via the briefly
	// lagging branch-ref lookup — read-your-writes for the sole writer.
	if r := db.fileReader.consistent; r != nil && r.head != nil {
		r.head.set(newSHA)
	}
	return nil
}

// Compile-time check: BatchingGitHubDB satisfies dal.Backend. The embedded
// *githubDB supplies every method except the overridden
// RunReadwriteTransaction. BatchingGitHubDB is itself a Backend composed over
// another Backend (githubDB), not a decorator over a sealed dal.DB — callers
// that need a sealed dal.DB wrap the result with dal.NewDB themselves, the
// same as they would for githubDB.
var _ dal.Backend = (*BatchingGitHubDB)(nil)

// batchingTx implements dal.ReadwriteTransaction by buffering all writes.
//
// SingleRecord collections: each Set / Insert encodes the new record
// content and stores a TreeChange in bufferedFiles; Delete stores a
// TreeChange with nil Content (deletion).
//
// MapOfRecords collections: the in-flight state of each map file lives in
// workingMaps. On first touch we read the current remote state via
// ensureMapLoaded; subsequent Set / Insert / Delete modify the in-memory
// map. At flush time, every modified working map is re-encoded into one
// TreeChange.
type batchingTx struct {
	readonlyTx
	opts          dal.TransactionOptions
	bufferedFiles map[string]TreeChange
	workingMaps   map[string]map[string]map[string]any
	mapColDefs    map[string]*ingitdb.CollectionDef
	mapLoaded     map[string]bool
}

var _ dal.ReadwriteTransaction = (*batchingTx)(nil)

// Options returns the transaction options, so a worker can read or update the
// commit message via tx.Options().Message() / SetMessage during execution.
func (t *batchingTx) Options() dal.TransactionOptions { return t.opts }

func (t *batchingTx) Set(ctx context.Context, record dalrecord.Record) error {
	colDef, recordKey, err := t.resolveCollection(record.Key())
	if err != nil {
		return err
	}
	recordPath := resolveRecordPath(colDef, recordKey)
	record.SetError(nil)
	data, ok := record.Data().(map[string]any)
	if !ok {
		return fmt.Errorf("record data is not map[string]any")
	}
	switch colDef.RecordFile.RecordType {
	case ingitdb.MapOfRecords:
		if loadErr := t.ensureMapLoaded(ctx, recordPath, colDef); loadErr != nil {
			return loadErr
		}
		t.workingMaps[recordPath][recordKey] = ingitdb.ApplyLocaleToWrite(data, colDef.Columns)
		return nil
	default:
		encoded, encodeErr := encodeRecordContent(data, colDef.RecordFile.Format)
		if encodeErr != nil {
			return encodeErr
		}
		t.bufferedFiles[recordPath] = TreeChange{Path: recordPath, Content: encoded}
		return nil
	}
}

func (t *batchingTx) Insert(ctx context.Context, record dalrecord.Record, opts ...dal.InsertOption) error {
	_ = opts
	colDef, recordKey, err := t.resolveCollection(record.Key())
	if err != nil {
		return err
	}
	recordPath := resolveRecordPath(colDef, recordKey)
	data, ok := record.Data().(map[string]any)
	if !ok {
		return fmt.Errorf("record data is not map[string]any")
	}
	switch colDef.RecordFile.RecordType {
	case ingitdb.MapOfRecords:
		if loadErr := t.ensureMapLoaded(ctx, recordPath, colDef); loadErr != nil {
			return loadErr
		}
		if _, exists := t.workingMaps[recordPath][recordKey]; exists {
			return fmt.Errorf("record already exists: %s/%s", colDef.ID, recordKey)
		}
		record.SetError(nil)
		t.workingMaps[recordPath][recordKey] = ingitdb.ApplyLocaleToWrite(data, colDef.Columns)
		return nil
	default:
		// Check buffered state first: a buffered non-nil Content means the
		// record was Set / Inserted earlier in this tx → collision.
		if existing, has := t.bufferedFiles[recordPath]; has && existing.Content != nil {
			return fmt.Errorf("record already exists: %s/%s", colDef.ID, recordKey)
		}
		// If buffered as deletion, the remote-side file is logically gone
		// for the rest of this tx; allow re-insert.
		bufferedAsDelete := false
		if existing, has := t.bufferedFiles[recordPath]; has && existing.Content == nil {
			bufferedAsDelete = true
		}
		if !bufferedAsDelete {
			_, _, found, readErr := t.db.fileReader.readFileWithSHA(ctx, recordPath)
			if readErr != nil {
				return readErr
			}
			if found {
				return fmt.Errorf("record already exists: %s/%s", colDef.ID, recordKey)
			}
		}
		record.SetError(nil)
		encoded, encodeErr := encodeRecordContent(data, colDef.RecordFile.Format)
		if encodeErr != nil {
			return encodeErr
		}
		t.bufferedFiles[recordPath] = TreeChange{Path: recordPath, Content: encoded}
		return nil
	}
}

func (t *batchingTx) Delete(ctx context.Context, key *dalrecord.Key) error {
	colDef, recordKey, err := t.resolveCollection(key)
	if err != nil {
		return err
	}
	recordPath := resolveRecordPath(colDef, recordKey)
	// Delete is idempotent per the dalgo contract: deleting a record that does
	// not exist is a no-op (returns nil), never ErrRecordNotFound.
	switch colDef.RecordFile.RecordType {
	case ingitdb.MapOfRecords:
		if loadErr := t.ensureMapLoaded(ctx, recordPath, colDef); loadErr != nil {
			return loadErr
		}
		if _, exists := t.workingMaps[recordPath][recordKey]; !exists {
			return nil // idempotent: nothing to delete
		}
		delete(t.workingMaps[recordPath], recordKey)
		return nil
	default:
		if existing, has := t.bufferedFiles[recordPath]; has {
			if existing.Content == nil {
				return nil // already buffered as deleted — idempotent
			}
			// Was buffered as a write earlier; convert to delete.
			t.bufferedFiles[recordPath] = TreeChange{Path: recordPath, Content: nil}
			return nil
		}
		_, _, found, readErr := t.db.fileReader.readFileWithSHA(ctx, recordPath)
		if readErr != nil {
			return readErr
		}
		if !found {
			return nil // idempotent: nothing to delete
		}
		t.bufferedFiles[recordPath] = TreeChange{Path: recordPath, Content: nil}
		return nil
	}
}

// ensureMapLoaded reads the current remote state of a MapOfRecords file into
// workingMaps[recordPath] if not already loaded. Missing files load as an
// empty map so first-touch Insert / Set creates the file.
func (t *batchingTx) ensureMapLoaded(ctx context.Context, recordPath string, colDef *ingitdb.CollectionDef) error {
	if t.mapLoaded[recordPath] {
		return nil
	}
	content, _, found, readErr := t.db.fileReader.readFileWithSHA(ctx, recordPath)
	if readErr != nil {
		return readErr
	}
	var loaded map[string]map[string]any
	if !found {
		loaded = make(map[string]map[string]any)
	} else {
		parsed, parseErr := ingitdb.ParseMapOfRecordsContent(content, colDef.RecordFile.Format)
		if parseErr != nil {
			return parseErr
		}
		loaded = parsed
	}
	t.workingMaps[recordPath] = loaded
	t.mapColDefs[recordPath] = colDef
	t.mapLoaded[recordPath] = true
	return nil
}

// flushChanges computes the final []TreeChange list. SingleRecord entries
// are already in bufferedFiles and pass through unchanged. MapOfRecords
// working maps are encoded into one TreeChange per modified file. A map
// that has been emptied is written as a deletion of the underlying file
// (consistent with the spec's "leave no trace" semantics for drop and
// truncate-style behaviors).
func (t *batchingTx) flushChanges() ([]TreeChange, error) {
	changes := make([]TreeChange, 0, len(t.bufferedFiles)+len(t.workingMaps))
	for _, ch := range t.bufferedFiles {
		changes = append(changes, ch)
	}
	for recordPath, working := range t.workingMaps {
		colDef := t.mapColDefs[recordPath]
		if len(working) == 0 {
			changes = append(changes, TreeChange{Path: recordPath, Content: nil})
			continue
		}
		encoded, encodeErr := ingitdb.EncodeMapOfRecordsContent(
			working, colDef.RecordFile.Format, colDef.ID, colDef.ColumnsOrder)
		if encodeErr != nil {
			return nil, fmt.Errorf("encode map for %s: %w", recordPath, encodeErr)
		}
		changes = append(changes, TreeChange{Path: recordPath, Content: encoded})
	}
	return changes, nil
}

// SetMulti / DeleteMulti / Update / UpdateRecord / UpdateMulti / InsertMulti
// follow the upstream readwriteTx behavior: they are not implemented and
// return an error. Set-mode callers loop Set / Delete instead.

func (t *batchingTx) SetMulti(ctx context.Context, records []dalrecord.Record) error {
	_, _ = ctx, records
	return fmt.Errorf("not implemented by %s (batching)", DatabaseID)
}

func (t *batchingTx) DeleteMulti(ctx context.Context, keys []*dalrecord.Key) error {
	_, _ = ctx, keys
	return fmt.Errorf("not implemented by %s (batching)", DatabaseID)
}

// Update applies field-level updates as a buffered read-modify-write. It loads
// the record's CURRENT data (preferring buffered in-tx content when this key was
// Set/Updated earlier in the tx, else reading the current file consistently via
// the Git Data API), applies updates in memory, then buffers the result as a
// write (exactly like Set). Unlike Delete, Update is NOT idempotent: updating a
// record that does not exist returns record.ErrRecordNotFound.
func (t *batchingTx) Update(ctx context.Context, key *dalrecord.Key, updates []update.Update, preconditions ...dal.Precondition) error {
	if len(preconditions) > 0 {
		return fmt.Errorf("%w: Update preconditions are not supported by %s (batching)", dal.ErrNotSupported, DatabaseID)
	}
	colDef, recordKey, err := t.resolveCollection(key)
	if err != nil {
		if errors.Is(err, errCollectionNotInDefinition) {
			// An unknown collection cannot hold the record → not-found (Update is
			// not idempotent).
			return dalrecord.ErrRecordNotFound
		}
		return err
	}
	recordPath := resolveRecordPath(colDef, recordKey)

	switch colDef.RecordFile.RecordType {
	case ingitdb.MapOfRecords:
		if loadErr := t.ensureMapLoaded(ctx, recordPath, colDef); loadErr != nil {
			return loadErr
		}
		existing, exists := t.workingMaps[recordPath][recordKey]
		if !exists {
			return dalrecord.ErrRecordNotFound
		}
		data := ingitdb.ApplyLocaleToRead(existing, colDef.Columns)
		if applyErr := applyUpdates(data, updates); applyErr != nil {
			return applyErr
		}
		t.workingMaps[recordPath][recordKey] = ingitdb.ApplyLocaleToWrite(data, colDef.Columns)
		return nil
	default:
		data, found, loadErr := t.loadSingleForUpdate(ctx, recordPath, colDef)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return dalrecord.ErrRecordNotFound
		}
		if applyErr := applyUpdates(data, updates); applyErr != nil {
			return applyErr
		}
		encoded, encodeErr := encodeRecordContent(data, colDef.RecordFile.Format)
		if encodeErr != nil {
			return encodeErr
		}
		t.bufferedFiles[recordPath] = TreeChange{Path: recordPath, Content: encoded}
		return nil
	}
}

// loadSingleForUpdate returns the current data for a SingleRecord key, preferring
// the in-transaction buffered content (if this key was Set/Updated earlier in
// the tx) over a fresh consistent repo read. A key buffered as a deletion, or a
// missing repo file, reports found=false.
func (t *batchingTx) loadSingleForUpdate(ctx context.Context, recordPath string, colDef *ingitdb.CollectionDef) (map[string]any, bool, error) {
	if buffered, has := t.bufferedFiles[recordPath]; has {
		if buffered.Content == nil {
			return nil, false, nil // buffered as deletion → logically absent
		}
		data, parseErr := ingitdb.ParseRecordContentForCollection(buffered.Content, colDef)
		if parseErr != nil {
			return nil, false, parseErr
		}
		return data, true, nil
	}
	return t.readSingleRecord(ctx, recordPath, colDef)
}

func (t *batchingTx) UpdateRecord(ctx context.Context, record dalrecord.Record, updates []update.Update, preconditions ...dal.Precondition) error {
	return t.Update(ctx, record.Key(), updates, preconditions...)
}

func (t *batchingTx) UpdateMulti(ctx context.Context, keys []*dalrecord.Key, updates []update.Update, preconditions ...dal.Precondition) error {
	_, _, _, _ = ctx, keys, updates, preconditions
	return fmt.Errorf("not implemented by %s (batching)", DatabaseID)
}

func (t *batchingTx) InsertMulti(ctx context.Context, records []dalrecord.Record, opts ...dal.InsertOption) error {
	_, _, _ = ctx, records, opts
	return fmt.Errorf("not implemented by %s (batching)", DatabaseID)
}

func (t *batchingTx) ID() string {
	return ""
}
