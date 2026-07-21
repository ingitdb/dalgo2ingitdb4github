package dalgo2ghingitdb

import (
	"context"
	"errors"
	"fmt"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/ingitdb/ingitdb-go/ingitdb"
)

// executeQueryToRecordsReader executes a dal.StructuredQuery against a single
// collection by scanning the collection's record files from the GitHub repo
// tree, then applying WHERE / ORDER BY / LIMIT (and optionally keys-only /
// IntoRecord shaping) in memory. It returns a slice-backed dal.RecordsReader.
//
// The WHERE / ORDER BY / LIMIT semantics mirror the filesystem driver
// (github.com/ingitdb/dalgo2ingitdb query.go): comparison operators
// ==, <, <=, >, >=, In against a FieldRef vs Constant; AND group conditions;
// stable order-by with mixed numeric/string comparison; positive LIMIT.
// Records are sourced from the GitHub tree (TreeWriter.ListFilesUnder plus
// fileReader reads) instead of the local filesystem.
func (r readonlyTx) executeQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	sq, ok := query.(dal.StructuredQuery)
	if !ok {
		return nil, fmt.Errorf("%w: only dal.StructuredQuery is supported, got %T", dal.ErrNotSupported, query)
	}
	if sq.Offset() > 0 {
		return nil, fmt.Errorf("%w: OFFSET is not supported by %s", dal.ErrNotSupported, DatabaseID)
	}
	if sq.StartFrom() != "" {
		return nil, fmt.Errorf("%w: cursor (START FROM) is not supported by %s", dal.ErrNotSupported, DatabaseID)
	}
	if len(sq.GroupBy()) > 0 {
		return nil, fmt.Errorf("%w: GROUP BY is not supported by %s", dal.ErrNotSupported, DatabaseID)
	}
	if sq.Having() != nil {
		return nil, fmt.Errorf("%w: HAVING is not supported by %s", dal.ErrNotSupported, DatabaseID)
	}
	if len(sq.Columns()) > 0 {
		return nil, fmt.Errorf("%w: column projection is not supported by %s", dal.ErrNotSupported, DatabaseID)
	}

	colDef, err := r.collectionFromQuery(sq)
	if err != nil {
		return nil, err
	}
	if colDef == nil {
		// Unknown collection (not in the definition): querying a collection that
		// does not exist yields no rows, matching the filesystem driver.
		return dal.EmptyReader{}, nil
	}

	rows, err := r.scanCollectionRecords(ctx, colDef)
	if err != nil {
		return nil, err
	}

	// Build data-backed records first so WHERE / ORDER BY can read fields;
	// for keys-only queries the data is stripped at the end, after filtering.
	records := make([]record.Record, 0, len(rows))
	for _, row := range rows {
		records = append(records, buildQueryRecord(colDef.ID, row))
	}

	if cond := sq.Where(); cond != nil {
		records, err = applyWhere(records, cond)
		if err != nil {
			return nil, err
		}
	}
	if orderBy := sq.OrderBy(); len(orderBy) > 0 {
		applyOrderBy(records, orderBy)
	}
	if limit := sq.Limit(); limit > 0 && len(records) > limit {
		records = records[:limit]
	}

	if sq.IntoRecord() == nil && sq.IDKind() != reflect.Invalid {
		for i, rec := range records {
			keyOnly := record.NewRecord(rec.Key())
			keyOnly.SetError(nil)
			records[i] = keyOnly
		}
	}

	return dal.NewRecordsReader(records), nil
}

// keyedRow is a decoded record plus its derived record key.
type keyedRow struct {
	key  string
	data map[string]any
}

// collectionFromQuery resolves the collection definition a structured query
// reads from. It returns (nil, nil) when the FROM collection is not present in
// the loaded definition so the caller can return an empty reader (matching the
// filesystem driver's errCollectionNotInDefinition handling). Any other
// resolution failure (nested subcollection missing, missing record_file) is a
// hard error.
func (r readonlyTx) collectionFromQuery(sq dal.StructuredQuery) (*ingitdb.CollectionDef, error) {
	if r.db.def == nil {
		return nil, fmt.Errorf("definition is required")
	}
	fromSrc := sq.From()
	if fromSrc == nil {
		return nil, fmt.Errorf("query has no FROM clause")
	}
	base := fromSrc.Base()
	colRef, ok := base.(dal.CollectionRef)
	if !ok {
		return nil, fmt.Errorf("FROM source must be a dal.CollectionRef, got %T", base)
	}
	collectionID := colRef.Name()
	parent := colRef.Parent()
	// A top-level collection absent from the definition means "no such
	// collection" → empty result. Detect it before resolveScopedCollection so
	// we can distinguish it from a genuine nested-resolution error.
	if parent == nil {
		if _, present := r.db.def.Collections[collectionID]; !present {
			return nil, nil
		}
	}
	// resolveScopedCollection scopes the DirPath to the parent record chain so a
	// nested subcollection query reads only that parent's records
	// (spaces/family/contacts/...), exactly as Get does. Top-level queries
	// (parent == nil) are a flat lookup.
	colDef, err := resolveScopedCollection(r.db.def, collectionID, parent)
	if err != nil {
		if errors.Is(err, errCollectionNotInDefinition) {
			// Unknown (sub)collection → no such collection → empty result, matching
			// the filesystem driver's errCollectionNotInDefinition handling.
			return nil, nil
		}
		return nil, err
	}
	if colDef.RecordFile == nil {
		return nil, fmt.Errorf("collection %q has no record file", collectionID)
	}
	return colDef, nil
}

// scanCollectionRecords reads every record of the collection from the GitHub
// tree and returns each decoded row with its derived record key.
func (r readonlyTx) scanCollectionRecords(ctx context.Context, colDef *ingitdb.CollectionDef) ([]keyedRow, error) {
	switch colDef.RecordFile.RecordType {
	case ingitdb.SingleRecord:
		return r.scanSingleRecords(ctx, colDef)
	case ingitdb.MapOfRecords:
		return r.scanMapOfRecords(ctx, colDef)
	default:
		return nil, fmt.Errorf("query unsupported for record type %q", colDef.RecordFile.RecordType)
	}
}

// scanSingleRecords lists every blob under the collection's records directory,
// derives each record's key from its file path (strip the base-dir prefix +
// expand the {key} template, mirroring the filesystem driver's key extractor),
// reads and decodes each file, and returns the keyed rows.
//
// The records directory is path.Join(colDef.DirPath, RecordsBasePath()), which
// is exactly the prefix resolveRecordPath produces before appending the record
// file name — so the scan reads from where Set writes.
func (r readonlyTx) scanSingleRecords(ctx context.Context, colDef *ingitdb.CollectionDef) ([]keyedRow, error) {
	treeWriter, err := NewTreeWriter(r.db.cfg)
	if err != nil {
		return nil, err
	}
	basePath := path.Join(colDef.DirPath, colDef.RecordFile.RecordsBasePath())
	files, err := treeWriter.ListFilesUnder(ctx, basePath)
	if err != nil {
		return nil, err
	}
	keyExtractor, err := buildKeyExtractor(colDef.RecordFile.Name)
	if err != nil {
		return nil, err
	}
	rows := make([]keyedRow, 0, len(files))
	prefix := strings.Trim(basePath, "/") + "/"
	for _, filePath := range files {
		clean := strings.TrimPrefix(strings.Trim(filePath, "/"), prefix)
		if colDef.RecordFile.IsExcluded(path.Base(clean)) {
			continue
		}
		recordKey := keyExtractor(clean)
		if recordKey == "" {
			continue
		}
		data, found, readErr := r.readSingleRecord(ctx, filePath, colDef)
		if readErr != nil {
			return nil, readErr
		}
		if !found {
			continue
		}
		rows = append(rows, keyedRow{key: recordKey, data: data})
	}
	return rows, nil
}

// scanMapOfRecords reads the single map-of-records file and returns one keyed
// row per map entry.
func (r readonlyTx) scanMapOfRecords(ctx context.Context, colDef *ingitdb.CollectionDef) ([]keyedRow, error) {
	recordPath := resolveRecordPath(colDef, "")
	content, found, err := r.db.fileReader.ReadFile(ctx, recordPath)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	allRecords, parseErr := ingitdb.ParseMapOfRecordsContent(content, colDef.RecordFile.Format)
	if parseErr != nil {
		return nil, parseErr
	}
	rows := make([]keyedRow, 0, len(allRecords))
	for id, fields := range allRecords {
		localized := ingitdb.ApplyLocaleToRead(fields, colDef.Columns)
		rows = append(rows, keyedRow{key: id, data: localized})
	}
	return rows, nil
}

// buildQueryRecord converts a decoded row into a record.Record shaped for the
// query: keys-only records carry only the key; IntoRecord/default records carry
// the row's data map. The record's key ID is the derived record key.
func buildQueryRecord(collection string, row keyedRow) record.Record {
	key := record.NewKeyWithID(collection, row.key)
	// Always back the record with the decoded map so the shared WHERE /
	// ORDER BY helpers can read fields. Keys-only queries strip the data
	// after filtering (a keys-only WHERE still needs the row's fields).
	rec := record.NewRecordWithData(key, row.data)
	rec.SetError(nil)
	return rec
}

// buildKeyExtractor returns a function that recovers a record key from a file
// path relative to the records base directory, expanding the {key} placeholder
// in the record-file name template. Mirrors the filesystem driver's extractor.
func buildKeyExtractor(nameTemplate string) (func(relPath string) string, error) {
	idx := strings.Index(nameTemplate, "{key}")
	if idx < 0 {
		return func(relPath string) string {
			base := path.Base(relPath)
			return strings.TrimSuffix(base, path.Ext(base))
		}, nil
	}
	prefix := regexp.QuoteMeta(nameTemplate[:idx])
	remainder := nameTemplate[idx+len("{key}"):]
	suffix := regexp.QuoteMeta(strings.ReplaceAll(remainder, "{key}", "\x00"))
	suffix = strings.ReplaceAll(suffix, "\x00", ".*")
	re, err := regexp.Compile("^" + prefix + "(.*?)" + suffix + "$")
	if err != nil {
		return nil, fmt.Errorf("build key extractor for %q: %w", nameTemplate, err)
	}
	return func(relPath string) string {
		m := re.FindStringSubmatch(relPath)
		if m == nil {
			return ""
		}
		return m[1]
	}, nil
}

// applyWhere filters records by the query condition, reading fields from the
// record's data map (and $id from the key).
func applyWhere(records []record.Record, cond dal.Condition) ([]record.Record, error) {
	filtered := records[:0]
	for _, rec := range records {
		data := recordData(rec)
		recKey := fmt.Sprintf("%v", rec.Key().ID)
		match, err := evaluateCondition(cond, data, recKey)
		if err != nil {
			return nil, err
		}
		if match {
			filtered = append(filtered, rec)
		}
	}
	return filtered, nil
}

// applyOrderBy sorts records in place, stably, honoring asc/desc per field.
func applyOrderBy(records []record.Record, orderBy []dal.OrderExpression) {
	sort.SliceStable(records, func(i, j int) bool {
		dataI := recordData(records[i])
		dataJ := recordData(records[j])
		keyI := fmt.Sprintf("%v", records[i].Key().ID)
		keyJ := fmt.Sprintf("%v", records[j].Key().ID)
		for _, expr := range orderBy {
			fieldRef, isRef := expr.Expression().(dal.FieldRef)
			if !isRef {
				continue
			}
			fieldName := fieldRef.Name()
			var vI, vJ any
			if fieldName == "$id" {
				vI, vJ = keyI, keyJ
			} else {
				vI = dataI[fieldName]
				vJ = dataJ[fieldName]
			}
			cmp := compareValues(vI, vJ)
			if cmp == 0 {
				continue
			}
			if expr.Descending() {
				return cmp > 0
			}
			return cmp < 0
		}
		return false
	})
}

// recordData returns the record's data map, or an empty map when absent.
func recordData(rec record.Record) map[string]any {
	if data, ok := rec.Data().(map[string]any); ok {
		return data
	}
	return map[string]any{}
}

func evaluateCondition(cond dal.Condition, data map[string]any, recordKey string) (bool, error) {
	switch c := cond.(type) {
	case dal.Comparison:
		return evaluateComparison(c, data, recordKey)
	case dal.GroupCondition:
		return evaluateGroupCondition(c, data, recordKey)
	default:
		return false, fmt.Errorf("unsupported condition type %T", cond)
	}
}

func evaluateGroupCondition(gc dal.GroupCondition, data map[string]any, recordKey string) (bool, error) {
	switch gc.Operator() {
	case dal.And:
		for _, cond := range gc.Conditions() {
			match, err := evaluateCondition(cond, data, recordKey)
			if err != nil {
				return false, err
			}
			if !match {
				return false, nil
			}
		}
		return true, nil
	default:
		// The public dal API only builds And group conditions; Or is reachable
		// only via unexported APIs, so any other operator is unsupported here.
		return false, fmt.Errorf("unsupported group operator %q", gc.Operator())
	}
}

func evaluateComparison(c dal.Comparison, data map[string]any, recordKey string) (bool, error) {
	leftVal, err := resolveExpression(c.Left, data, recordKey)
	if err != nil {
		return false, err
	}
	// The In operator compares the left value against a set of constants.
	if c.Operator == dal.In {
		return evaluateIn(leftVal, c.Right)
	}
	rightVal, err := resolveExpression(c.Right, data, recordKey)
	if err != nil {
		return false, err
	}
	cmp := compareValues(leftVal, rightVal)
	switch c.Operator {
	case dal.Equal:
		return cmp == 0, nil
	case dal.GreaterThen:
		return cmp > 0, nil
	case dal.GreaterOrEqual:
		return cmp >= 0, nil
	case dal.LessThen:
		return cmp < 0, nil
	case dal.LessOrEqual:
		return cmp <= 0, nil
	default:
		return false, fmt.Errorf("unsupported operator %q", c.Operator)
	}
}

// evaluateIn reports whether leftVal equals any element of the set on the
// right. The dal query builder represents an IN set as a dal.Array whose Value
// is a typed slice ([]string, []int, ...); a dal.Constant is also accepted for
// a single-value set.
func evaluateIn(leftVal any, right dal.Expression) (bool, error) {
	for _, candidate := range inValues(right) {
		if compareValues(leftVal, candidate) == 0 {
			return true, nil
		}
	}
	return false, nil
}

// inValues flattens the right-hand side of an IN comparison into a slice of
// candidate values.
func inValues(right dal.Expression) []any {
	switch r := right.(type) {
	case dal.Array:
		return sliceToAny(r.Value)
	case dal.Constant:
		return sliceToAny(r.Value)
	default:
		return nil
	}
}

// sliceToAny reflects over a typed slice (or wraps a scalar) into []any so the
// elements can be compared uniformly.
func sliceToAny(v any) []any {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return []any{v}
	}
	out := make([]any, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		out[i] = rv.Index(i).Interface()
	}
	return out
}

func resolveExpression(expr dal.Expression, data map[string]any, recordKey string) (any, error) {
	switch e := expr.(type) {
	case dal.FieldRef:
		if e.Name() == "$id" {
			return recordKey, nil
		}
		return data[e.Name()], nil
	case dal.Constant:
		return e.Value, nil
	default:
		return nil, fmt.Errorf("unsupported expression type %T", expr)
	}
}

// compareValues compares two values: numerically when both are numeric,
// otherwise by their string form. Mirrors the filesystem driver.
func compareValues(a, b any) int {
	aFloat, aIsNum := toFloat64(a)
	bFloat, bIsNum := toFloat64(b)
	if aIsNum && bIsNum {
		switch {
		case aFloat < bFloat:
			return -1
		case aFloat > bFloat:
			return 1
		default:
			return 0
		}
	}
	aStr := fmt.Sprintf("%v", a)
	bStr := fmt.Sprintf("%v", b)
	return strings.Compare(aStr, bStr)
}

func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	default:
		return 0, false
	}
}
