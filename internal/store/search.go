package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	sqlitevec "github.com/asg017/sqlite-vec-go-bindings/cgo"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/lexical"
	"github.com/HW-Yue/Memora/internal/lexicallocation"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
)

// indexJob is a vector embedding to compute after commit. Embedding calls a
// remote model, so it never runs inside the write transaction.
type indexJob struct {
	kind, objectID, databaseID, tableID, text string
	remove                                    bool
}

// ---- lexical postings (in-transaction) ----

func (t *tx) replacePostings(ctx context.Context, kind, objectID, databaseID, tableID string, revision uint64, fields map[string][]string) error {
	if _, err := t.q().ExecContext(ctx, `DELETE FROM mem_postings WHERE object_kind = ? AND object_id = ?`, kind, objectID); err != nil {
		return err
	}
	counts := map[[2]string]uint64{}
	for fieldID, values := range fields {
		for _, value := range values {
			terms, err := lexical.Terms(value)
			if err != nil {
				continue
			}
			for _, term := range terms {
				counts[[2]string{term, fieldID}]++
			}
		}
	}
	for key, frequency := range counts {
		if _, err := t.q().ExecContext(ctx, `INSERT INTO mem_postings(term, object_kind, object_id, database_id, table_id, revision, field_id, frequency)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, key[0], kind, objectID, databaseID, tableID, revision, key[1], frequency); err != nil {
			return err
		}
	}
	return nil
}

func (t *tx) unindex(ctx context.Context, kind, objectID string) error {
	if _, err := t.q().ExecContext(ctx, `DELETE FROM mem_postings WHERE object_kind = ? AND object_id = ?`, kind, objectID); err != nil {
		return err
	}
	t.indexed = append(t.indexed, indexJob{kind: kind, objectID: objectID, remove: true})
	return nil
}

func (t *tx) indexRow(ctx context.Context, table catalog.Table, value storedRow) error {
	if value.State != row.StateLive {
		return t.unindex(ctx, "row", value.ID)
	}
	fields := map[string][]string{}
	texts := []string{}
	for _, column := range table.Columns {
		if column.Archived() {
			continue
		}
		raw, ok := value.Values[column.ID]
		if !ok || raw == nil {
			continue
		}
		text := strings.TrimSpace(fmt.Sprint(raw))
		if text == "" {
			continue
		}
		fields[column.ID] = []string{text}
		texts = append(texts, column.Name+": "+text)
	}
	if err := t.replacePostings(ctx, "row", value.ID, table.DatabaseID, table.ID, value.Revision, fields); err != nil {
		return err
	}
	t.indexed = append(t.indexed, indexJob{kind: "row", objectID: value.ID, databaseID: table.DatabaseID, tableID: table.ID,
		text: table.Name + " — " + table.RowSemantics + "\n" + strings.Join(texts, "\n")})
	return nil
}

func (t *tx) indexRoute(ctx context.Context, table catalog.Table, node routeNode) error {
	fields := map[string][]string{
		"name": {node.Name}, "kind": {string(node.Kind)}, "purpose": {node.Purpose},
	}
	if len(node.Aliases) > 0 {
		fields["aliases"] = node.Aliases
	}
	if strings.TrimSpace(node.Synopsis) != "" {
		fields["synopsis"] = []string{node.Synopsis}
	}
	if err := t.replacePostings(ctx, "route", node.ID, table.DatabaseID, table.ID, node.Revision, fields); err != nil {
		return err
	}
	text := strings.Join([]string{table.Name, node.Name, strings.Join(node.Aliases, ", "), node.Purpose, node.Synopsis}, "\n")
	t.indexed = append(t.indexed, indexJob{kind: "route", objectID: node.ID, databaseID: table.DatabaseID, tableID: table.ID, text: text})
	return nil
}

// PostingsInDatabases implements lexicallocation.Source.
func (db *DB) PostingsInDatabases(ctx context.Context, terms, databaseIDs []string) ([]fulltext.Posting, error) {
	if len(terms) == 0 || len(databaseIDs) == 0 {
		return []fulltext.Posting{}, nil
	}
	postings := []fulltext.Posting{}
	err := db.view(ctx, func(t *tx) error {
		query := `SELECT term, object_kind, database_id, table_id, object_id, revision, field_id, frequency FROM mem_postings
			WHERE term IN (` + placeholders(len(terms)) + `) AND database_id IN (` + placeholders(len(databaseIDs)) + `)`
		arguments := make([]any, 0, len(terms)+len(databaseIDs))
		for _, term := range terms {
			arguments = append(arguments, term)
		}
		for _, id := range databaseIDs {
			arguments = append(arguments, id)
		}
		rows, err := t.q().QueryContext(ctx, query, arguments...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var posting fulltext.Posting
			var kind string
			if err := rows.Scan(&posting.Term, &kind, &posting.DatabaseID, &posting.TableID, &posting.ObjectID,
				&posting.Revision, &posting.FieldID, &posting.Frequency); err != nil {
				return err
			}
			posting.Kind = fulltext.ObjectKind(kind)
			postings = append(postings, posting)
		}
		return rows.Err()
	})
	return postings, err
}

// SearchLexicalLocations implements executor.LexicalLocationReader.
func (db *DB) SearchLexicalLocations(ctx context.Context, request lexicallocation.Request) (lexicallocation.Page, error) {
	return lexicallocation.Search(ctx, db, request)
}

func placeholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

// ---- vectors (after commit) ----

func (db *DB) runIndexJobs(jobs []indexJob) {
	if len(jobs) == 0 {
		return
	}
	latest := map[string]indexJob{}
	order := []string{}
	for _, job := range jobs {
		key := job.kind + "\x00" + job.objectID
		if _, seen := latest[key]; !seen {
			order = append(order, key)
		}
		latest[key] = job
	}
	ordered := make([]indexJob, 0, len(order))
	for _, key := range order {
		ordered = append(ordered, latest[key])
	}
	db.indexing.Add(1)
	go func() {
		defer db.indexing.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := db.applyVectorJobs(ctx, ordered); err != nil {
			log.Printf("memora: vector indexing: %v", err)
		}
	}()
}

// WaitIndexing blocks until every scheduled vector update has finished.
func (db *DB) WaitIndexing() { db.indexing.Wait() }

func digestText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func (db *DB) applyVectorJobs(ctx context.Context, jobs []indexJob) error {
	if db.embedder == nil {
		return nil
	}
	db.vectorMu.Lock()
	defer db.vectorMu.Unlock()
	pending := []indexJob{}
	for _, job := range jobs {
		if job.remove {
			if err := db.removeVector(ctx, job.kind, job.objectID); err != nil {
				return err
			}
			continue
		}
		var digest string
		err := db.sql.QueryRowContext(ctx, `SELECT digest FROM mem_vector_items WHERE object_kind = ? AND object_id = ?`,
			job.kind, job.objectID).Scan(&digest)
		if err == nil && digest == digestText(job.text) {
			continue
		}
		pending = append(pending, job)
	}
	for start := 0; start < len(pending); start += 64 {
		batch := pending[start:min(start+64, len(pending))]
		texts := make([]string, len(batch))
		for index, job := range batch {
			texts[index] = job.text
		}
		vectors, err := db.embedder.Embed(ctx, texts)
		if err != nil {
			return err
		}
		if len(vectors) != len(batch) {
			return fmt.Errorf("embedder returned %d vectors for %d texts", len(vectors), len(batch))
		}
		for index, job := range batch {
			if err := db.storeVector(ctx, job, vectors[index]); err != nil {
				return err
			}
		}
	}
	return nil
}

func (db *DB) removeVector(ctx context.Context, kind, objectID string) error {
	db.write.Lock()
	defer db.write.Unlock()
	var id int64
	err := db.sql.QueryRowContext(ctx, `SELECT id FROM mem_vector_items WHERE object_kind = ? AND object_id = ?`, kind, objectID).Scan(&id)
	if err != nil {
		return nil
	}
	if _, err := db.sql.ExecContext(ctx, `DELETE FROM mem_vectors WHERE rowid = ?`, id); err != nil {
		return err
	}
	_, err = db.sql.ExecContext(ctx, `DELETE FROM mem_vector_items WHERE id = ?`, id)
	return err
}

func (db *DB) storeVector(ctx context.Context, job indexJob, vector []float32) error {
	blob, err := sqlitevec.SerializeFloat32(vector)
	if err != nil {
		return err
	}
	// Vector rows are written through the same serial writer as everything
	// else; a commit here between another writer's first read and first write
	// would invalidate that writer's snapshot.
	db.write.Lock()
	defer db.write.Unlock()
	handle, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = handle.Rollback() }()
	var id int64
	err = handle.QueryRowContext(ctx, `INSERT INTO mem_vector_items(object_kind, object_id, database_id, table_id, digest) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(object_kind, object_id) DO UPDATE SET digest = excluded.digest RETURNING id`,
		job.kind, job.objectID, job.databaseID, job.tableID, digestText(job.text)).Scan(&id)
	if err != nil {
		return err
	}
	if _, err := handle.ExecContext(ctx, `DELETE FROM mem_vectors WHERE rowid = ?`, id); err != nil {
		return err
	}
	if _, err := handle.ExecContext(ctx, `INSERT INTO mem_vectors(rowid, embedding) VALUES (?, ?)`, id, blob); err != nil {
		return err
	}
	return handle.Commit()
}

// VectorHit is one nearest-neighbour result.
type VectorHit struct {
	Kind       string
	ObjectID   string
	DatabaseID string
	TableID    string
	Distance   float64
}

// SearchVectors embeds query and returns the nearest objects of kind.
func (db *DB) SearchVectors(ctx context.Context, kind, query string, limit int) ([]VectorHit, error) {
	if db.embedder == nil {
		return nil, fail(result.CodeUnsupported, "vector search requires an embedding model; set MEMORA_EMBEDDING_API_KEY")
	}
	if strings.TrimSpace(query) == "" || limit < 1 {
		return nil, fail(result.CodeValidation, "vector search needs a query and a positive limit")
	}
	vectors, err := db.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fail(result.CodeInternal, "embedding the query failed: %v", err)
	}
	blob, err := sqlitevec.SerializeFloat32(vectors[0])
	if err != nil {
		return nil, err
	}
	// vec0 KNN has no pre-filter on kind, so over-fetch and filter.
	rows, err := db.sql.QueryContext(ctx, `SELECT i.object_kind, i.object_id, i.database_id, i.table_id, v.distance
		FROM (SELECT rowid, distance FROM mem_vectors WHERE embedding MATCH ? AND k = ?) v
		JOIN mem_vector_items i ON i.id = v.rowid ORDER BY v.distance`, blob, limit*4+16)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hits := []VectorHit{}
	for rows.Next() {
		var hit VectorHit
		if err := rows.Scan(&hit.Kind, &hit.ObjectID, &hit.DatabaseID, &hit.TableID, &hit.Distance); err != nil {
			return nil, err
		}
		if hit.Kind == kind && len(hits) < limit {
			hits = append(hits, hit)
		}
	}
	return hits, rows.Err()
}

// SearchRouteVectors returns the live route nodes nearest to query.
func (db *DB) SearchRouteVectors(ctx context.Context, query string, limit int) ([]router.Node, error) {
	hits, err := db.SearchVectors(ctx, "route", query, limit)
	if err != nil {
		return nil, err
	}
	nodes := []router.Node{}
	err = db.view(ctx, func(t *tx) error {
		for _, hit := range hits {
			node, err := t.getNode(ctx, hit.ObjectID)
			if err != nil || node.Deleted || node.Kind == router.KindRoot {
				continue
			}
			nodes = append(nodes, node)
		}
		return nil
	})
	return nodes, err
}

// Reindex rebuilds lexical postings and schedules vectors for everything live.
func (db *DB) Reindex(ctx context.Context) error {
	return db.update(ctx, func(t *tx) error {
		databases, err := t.loadDatabases(ctx)
		if err != nil {
			return err
		}
		for _, database := range databases {
			for _, table := range database.Tables {
				rows, err := t.q().QueryContext(ctx, `SELECT `+rowColumns+` FROM `+dataTable(table.ID)+` WHERE row_state = 'live'`)
				if err != nil {
					return err
				}
				values := []storedRow{}
				for rows.Next() {
					value, err := scanRow(rows)
					if err != nil {
						rows.Close()
						return err
					}
					values = append(values, value)
				}
				rows.Close()
				for _, value := range values {
					if err := t.indexRow(ctx, table, value); err != nil {
						return err
					}
				}
				nodes, err := t.tableRouteNodes(ctx, table.ID)
				if err != nil {
					return err
				}
				for _, node := range nodes {
					if err := t.indexRoute(ctx, table, node); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}

func (t *tx) tableRouteNodes(ctx context.Context, tableID string) ([]routeNode, error) {
	rows, err := t.q().QueryContext(ctx, `SELECT body FROM `+routeTable(tableID)+` WHERE deprecated = 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := []routeNode{}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var node routeNode
		if err := decodeJSON(body, &node); err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	sort.Slice(nodes, func(left, right int) bool { return nodes[left].ID < nodes[right].ID })
	return nodes, rows.Err()
}
