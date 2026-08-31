package nativecatalog

import (
	"github.com/HW-Yue/Memora/internal/catalog"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
	"github.com/HW-Yue/Memora/internal/store/objectindex"
)

// ObjectKinds are the Catalog's slots in the objects Tree's key space.
//
// They are the record log's own object kinds, so one Database has one kind
// wherever it is stored and the two indexes cannot drift into disagreeing about
// what it is. The Catalog owns these three kinds outright: a publication hands
// over the whole set, so the Tree's entries for them are replaced rather than
// merged.
var ObjectKinds = []uint16{
	uint16(nativestore.ObjectKindDatabase),
	uint16(nativestore.ObjectKindTable),
	uint16(nativestore.ObjectKindColumn),
}

// ObjectRecords encodes a Catalog for the objects Tree.
//
// The body is the record log's own encoding, byte for byte — the same
// encodeDatabase/encodeTable/encodeColumn the record log is written with,
// including the positional order each object was published at, which is what
// the reader sorts by. Storing a re-encoding would make the Tree a translation
// of the authority rather than a copy of it.
//
// The revision is the object's SchemaVersion, which is also what the Catalog
// Tree's Locator carries. A reader that finds the two disagreeing has found two
// Trees describing different Catalogs, and says so.
func ObjectRecords(databases []catalog.Database) ([]objectindex.Record, error) {
	records := make([]objectindex.Record, 0, len(databases))
	for databaseIndex, database := range databases {
		body, err := encodeDatabase(database, uint64(databaseIndex))
		if err != nil {
			return nil, err
		}
		records = append(records, objectindex.Record{
			Kind: uint16(nativestore.ObjectKindDatabase), ID: database.ID,
			Revision: database.SchemaVersion, Body: body,
		})
		for tableIndex, table := range database.Tables {
			body, err := encodeTable(table, uint64(tableIndex))
			if err != nil {
				return nil, err
			}
			records = append(records, objectindex.Record{
				Kind: uint16(nativestore.ObjectKindTable), ID: table.ID,
				Revision: table.SchemaVersion, Body: body,
			})
			for columnIndex, column := range table.Columns {
				body, err := encodeColumn(column, table.ID, uint64(columnIndex))
				if err != nil {
					return nil, err
				}
				records = append(records, objectindex.Record{
					Kind: uint16(nativestore.ObjectKindColumn), ID: column.ID,
					Revision: column.SchemaVersion, Body: body,
				})
			}
		}
	}
	return records, nil
}

// ObjectSource hands out the objects Tree that currently holds the Catalog
// bodies. It is asked on every read rather than captured once, because a COW
// rebuild replaces the generation, and with it the Tree.
type ObjectSource interface {
	CatalogObjects() *objectindex.Index
}
