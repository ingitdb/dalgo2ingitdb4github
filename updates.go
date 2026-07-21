package dalgo2ghingitdb

import (
	"fmt"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record/update"
)

// applyUpdates mutates data according to updates. It mirrors the filesystem
// driver (github.com/ingitdb/dalgo2ingitdb tx_readwrite.go) exactly. Supported:
//   - Single-segment FieldName updates
//   - Multi-segment FieldPath updates with auto-created intermediate maps
//   - update.DeleteField sentinel as Value() → deletes the (possibly nested) field
//   - dal increment transform → numeric add (nil/missing counts as 0)
//   - update.ServerTimestamp sentinel → RFC3339Nano UTC string
func applyUpdates(data map[string]any, updates []update.Update) error {
	for _, u := range updates {
		name := u.FieldName()
		if name != "" {
			if err := applyFieldUpdate(data, []string{name}, u.Value()); err != nil {
				return err
			}
			continue
		}
		fieldPath := u.FieldPath()
		if len(fieldPath) == 0 {
			// No name and no path — skip silently (defensive).
			continue
		}
		if err := applyFieldUpdate(data, fieldPath, u.Value()); err != nil {
			return err
		}
	}
	return nil
}

// applyFieldUpdate applies a single field update described by path and value to
// the provided map, navigating (and creating, for set operations) intermediate
// maps as needed.
func applyFieldUpdate(root map[string]any, fieldPath []string, value any) error {
	if value == update.DeleteField {
		return applyDelete(root, fieldPath)
	}
	if value == update.ServerTimestamp {
		value = time.Now().UTC().Format(time.RFC3339Nano)
	} else if t, ok := dal.IsTransform(value); ok {
		if t.Name() == "increment" {
			return applyIncrement(root, fieldPath, t.Value())
		}
		return fmt.Errorf("unsupported transform %q", t.Name())
	}

	m := root
	for i, segment := range fieldPath[:len(fieldPath)-1] {
		existing, ok := m[segment]
		if !ok || existing == nil {
			child := make(map[string]any)
			m[segment] = child
			m = child
			continue
		}
		child, isMap := existing.(map[string]any)
		if !isMap {
			return fmt.Errorf("field %q at path position %d is not a map (got %T)", segment, i, existing)
		}
		m = child
	}
	m[fieldPath[len(fieldPath)-1]] = value
	return nil
}

// applyDelete removes the leaf element at path from root. Missing intermediate
// maps or a missing leaf are treated as no-ops (idempotent).
func applyDelete(root map[string]any, fieldPath []string) error {
	m := root
	for _, segment := range fieldPath[:len(fieldPath)-1] {
		existing, ok := m[segment]
		if !ok || existing == nil {
			return nil
		}
		child, isMap := existing.(map[string]any)
		if !isMap {
			return nil
		}
		m = child
	}
	delete(m, fieldPath[len(fieldPath)-1])
	return nil
}

// applyIncrement adds the transform's numeric delta to the current value at
// path. A nil or missing field counts as 0; a non-numeric existing value is an
// error.
func applyIncrement(root map[string]any, fieldPath []string, delta any) error {
	m := root
	for i, segment := range fieldPath[:len(fieldPath)-1] {
		existing, ok := m[segment]
		if !ok || existing == nil {
			child := make(map[string]any)
			m[segment] = child
			m = child
			continue
		}
		child, isMap := existing.(map[string]any)
		if !isMap {
			return fmt.Errorf("field %q at path position %d is not a map (got %T)", segment, i, existing)
		}
		m = child
	}

	leaf := fieldPath[len(fieldPath)-1]
	existing := m[leaf]

	deltaF, ok := toNumericFloat64(delta)
	if !ok {
		return fmt.Errorf("increment delta %v (%T) is not numeric", delta, delta)
	}

	var currentF float64
	if existing != nil {
		currentF, ok = toNumericFloat64(existing)
		if !ok {
			return fmt.Errorf("increment target field %q has non-numeric value %v (%T)", leaf, existing, existing)
		}
	}

	result := currentF + deltaF

	// Preserve integer type when both delta and current were integral.
	if isIntegral(result) {
		switch delta.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			m[leaf] = int64(result)
			return nil
		}
	}
	m[leaf] = result
	return nil
}

// toNumericFloat64 converts a numeric value to float64. Returns (0, false) for
// non-numeric types.
func toNumericFloat64(v any) (float64, bool) {
	return toFloat64(v)
}

// isIntegral reports whether f represents a whole number.
func isIntegral(f float64) bool {
	return f == float64(int64(f))
}
