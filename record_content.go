package dalgo2ghingitdb

import (
	"fmt"
	"path"
	"strings"

	"github.com/ingitdb/ingitdb-go/ingitdb"
)

func resolveRecordPath(colDef *ingitdb.CollectionDef, recordKey string) string {
	recordName := strings.ReplaceAll(colDef.RecordFile.Name, "{key}", recordKey)
	base := colDef.RecordFile.RecordsBasePath()
	recordPath := path.Join(colDef.DirPath, base, recordName)
	return path.Clean(recordPath)
}

// checkWritePathFits refuses a write whose record file name, or a directory
// on the way to it (a parent record's directory), is longer than a file name
// can be on any checkout (ingitdb.MaxFileNameBytes). GitHub accepts such a
// tree; no local clone can create it. Keys are never shortened
// (record-key-encoding#req:over-long-key-fails).
func checkWritePathFits(colDef *ingitdb.CollectionDef, recordKey string) error {
	if err := colDef.RecordFile.CheckKeyFits(recordKey); err != nil {
		return fmt.Errorf("dalgo2ingitdb4github: %w", err)
	}
	for _, segment := range strings.Split(colDef.DirPath, "/") {
		if len(segment) > ingitdb.MaxFileNameBytes {
			return fmt.Errorf("dalgo2ingitdb4github: %w: the directory name %.40q... is %d bytes long, over the %d-byte limit of a file name, so no checkout can hold it; keys are never shortened: a parent record with such a key cannot have subcollections",
				ingitdb.ErrKeyNotStorable, segment, len(segment), ingitdb.MaxFileNameBytes)
		}
	}
	return nil
}
