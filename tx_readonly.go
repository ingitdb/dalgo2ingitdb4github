package dalgo2ghingitdb

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/recordset"
	"github.com/ingitdb/ingitdb-go/ingitdb"
)

var _ dal.ReadTransaction = (*readonlyTx)(nil)

type readonlyTx struct {
	db *githubDB
}

func (r readonlyTx) Options() dal.TransactionOptions {
	return nil
}

func (r readonlyTx) Get(ctx context.Context, record dal.Record) error {
	if r.db.def == nil {
		return fmt.Errorf("definition is required")
	}
	colDef, recordKey, err := r.resolveCollection(record.Key())
	if err != nil {
		if errors.Is(err, errCollectionNotInDefinition) {
			// A record in an unknown collection cannot exist: report not-found via
			// SetError and return nil so GetMulti keeps going, per the dalgo
			// Getter contract.
			record.SetError(dal.NewErrNotFoundByKey(record.Key(), nil))
			return nil
		}
		return err
	}
	recordPath := resolveRecordPath(colDef, recordKey)
	switch colDef.RecordFile.RecordType {
	case ingitdb.SingleRecord:
		data, found, err := r.readSingleRecord(ctx, recordPath, colDef)
		if err != nil {
			return err
		}
		if !found {
			record.SetError(dal.ErrRecordNotFound)
			return nil
		}
		record.SetError(nil)
		target := record.Data().(map[string]any)
		maps.Copy(target, data)
	case ingitdb.MapOfRecords:
		data, found, err := r.readRecordFromMap(ctx, recordPath, recordKey, colDef)
		if err != nil {
			return err
		}
		if !found {
			record.SetError(dal.ErrRecordNotFound)
			return nil
		}
		record.SetError(nil)
		target := record.Data().(map[string]any)
		maps.Copy(target, data)
	default:
		return fmt.Errorf("record type %q is not supported", colDef.RecordFile.RecordType)
	}
	return nil
}

func (r readonlyTx) Exists(ctx context.Context, key *dal.Key) (bool, error) {
	rec := dal.NewRecordWithData(key, map[string]any{})
	if err := r.Get(ctx, rec); err != nil {
		return false, err
	}
	// Get reports absence via record.SetError(<not-found>) and returns nil;
	// record.Exists() reflects that (false for a not-found record).
	return rec.Exists(), nil
}

// GetMulti loads each record by calling Get. Per-record not-found is reported
// via record.SetError (set inside Get), not as a batch-level error — matching
// the dalgo GetMulti contract. Only genuine errors abort the batch.
func (r readonlyTx) GetMulti(ctx context.Context, records []dal.Record) error {
	for _, rec := range records {
		if err := r.Get(ctx, rec); err != nil && !dal.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (r readonlyTx) ExecuteQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	return r.executeQueryToRecordsReader(ctx, query)
}

func (r readonlyTx) ExecuteQueryToRecordsetReader(ctx context.Context, query dal.Query, options ...recordset.Option) (dal.RecordsetReader, error) {
	_, _, _ = ctx, query, options
	return nil, fmt.Errorf("%w: recordset reader", dal.ErrNotSupported)
}

func (r readonlyTx) readSingleRecord(ctx context.Context, recordPath string, colDef *ingitdb.CollectionDef) (map[string]any, bool, error) {
	content, found, err := r.db.fileReader.ReadFile(ctx, recordPath)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}
	data, parseErr := ingitdb.ParseRecordContentForCollection(content, colDef)
	if parseErr != nil {
		return nil, false, parseErr
	}
	return data, true, nil
}

func (r readonlyTx) readRecordFromMap(ctx context.Context, recordPath, recordKey string, colDef *ingitdb.CollectionDef) (map[string]any, bool, error) {
	content, found, err := r.db.fileReader.ReadFile(ctx, recordPath)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}
	allRecords, parseErr := ingitdb.ParseMapOfRecordsContent(content, colDef.RecordFile.Format)
	if parseErr != nil {
		return nil, false, parseErr
	}
	recordData, exists := allRecords[recordKey]
	if !exists {
		return nil, false, nil
	}
	localizedData := ingitdb.ApplyLocaleToRead(recordData, colDef.Columns)
	return localizedData, true, nil
}

func (r readonlyTx) resolveCollection(key *dal.Key) (*ingitdb.CollectionDef, string, error) {
	if r.db.def == nil {
		return nil, "", fmt.Errorf("definition is required")
	}
	if key == nil {
		return nil, "", fmt.Errorf("key is nil")
	}
	// resolveScopedCollection walks the key's parent chain so nested records are
	// physically scoped under their parent record (spaces/family/contacts/...).
	// For a top-level key (no parent) it is a plain flat lookup, unchanged.
	colDef, err := resolveScopedCollection(r.db.def, key.Collection(), key.Parent())
	if err != nil {
		return nil, "", err
	}
	if colDef.RecordFile == nil {
		return nil, "", fmt.Errorf("collection %q has no record file", key.Collection())
	}
	recordKey := fmt.Sprintf("%v", key.ID)
	return colDef, recordKey, nil
}
