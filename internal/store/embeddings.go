package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
)

// SearchHit is one result of a cosine similarity search.
type SearchHit struct {
	ItemID string
	Kind   string
	Score  float64
}

// EncodeVector serializes a float32 vector to a little-endian byte slice, the
// on-disk form of the embeddings.vec BLOB column.
func EncodeVector(vec []float32) []byte {
	b := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(v))
	}
	return b
}

// DecodeVector reverses EncodeVector. A byte slice whose length is not a
// multiple of four yields nil (corrupt row; skip it).
func DecodeVector(b []byte) []float32 {
	if len(b)%4 != 0 {
		return nil
	}
	vec := make([]float32, len(b)/4)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return vec
}

// Cosine returns the cosine similarity of two equal-length vectors, in [-1, 1].
// Mismatched lengths or a zero-magnitude vector yield 0.
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		av, bv := float64(a[i]), float64(b[i])
		dot += av * bv
		na += av * av
		nb += bv * bv
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// UpsertEmbedding stores (or replaces) the vector for an item. dims is recorded
// from the vector length so a later model change is detectable.
func UpsertEmbedding(ctx context.Context, db *sql.DB, itemID, kind, model string, vec []float32) error {
	if itemID == "" {
		return fmt.Errorf("store.UpsertEmbedding: empty item_id")
	}
	if len(vec) == 0 {
		return fmt.Errorf("store.UpsertEmbedding: empty vector for %s", itemID)
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO embeddings (item_id, kind, model, dims, vec, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(item_id) DO UPDATE SET
		    kind=excluded.kind, model=excluded.model, dims=excluded.dims,
		    vec=excluded.vec, updated_at=excluded.updated_at`,
		itemID, kind, model, len(vec), EncodeVector(vec), core.FormatTime(time.Now().UTC()))
	if err != nil {
		return fmt.Errorf("store.UpsertEmbedding: %w", err)
	}
	return nil
}

// DeleteEmbedding removes an item's vector. A missing row is not an error.
func DeleteEmbedding(ctx context.Context, db *sql.DB, itemID string) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM embeddings WHERE item_id = ?`, itemID); err != nil {
		return fmt.Errorf("store.DeleteEmbedding: %w", err)
	}
	return nil
}

// CosineSearch brute-force-scans stored vectors for the given model and returns
// the top-limit most similar items, highest score first. An empty kinds filter
// searches all kinds. At the corpus scale this system targets (thousands of
// items) a full scan is milliseconds, which is why there is no vector index.
func CosineSearch(ctx context.Context, db *sql.DB, query []float32, model string, kinds []string, limit int) ([]SearchHit, error) {
	return cosineSearch(ctx, db, query, model, kinds, nil, limit)
}

// CosineSearchScoped is CosineSearch restricted to items whose project is in
// projects (recall passes the bound project plus "" for global). An empty
// projects filter searches all projects. Filtering inside the candidate query
// keeps the whole candidate depth in scope, so a corpus dominated by
// out-of-scope vectors cannot starve in-scope results out of the top-limit
// window. Embedding rows carry no project, so the scope is resolved by joining
// the index tables; an embedding orphaned from both indexes matches no scope.
func CosineSearchScoped(ctx context.Context, db *sql.DB, query []float32, model string, kinds, projects []string, limit int) ([]SearchHit, error) {
	return cosineSearch(ctx, db, query, model, kinds, projects, limit)
}

func cosineSearch(ctx context.Context, db *sql.DB, query []float32, model string, kinds, projects []string, limit int) ([]SearchHit, error) {
	if len(query) == 0 {
		return nil, fmt.Errorf("store.CosineSearch: empty query vector")
	}
	if limit <= 0 {
		limit = 10
	}

	var sqlStr string
	args := []any{model}
	if len(projects) == 0 {
		sqlStr = `SELECT item_id, kind, vec FROM embeddings WHERE model = ?`
		if len(kinds) > 0 {
			sqlStr += ` AND kind IN (` + placeholders(len(kinds)) + `)`
			for _, k := range kinds {
				args = append(args, k)
			}
		}
	} else {
		sqlStr = `SELECT e.item_id, e.kind, e.vec FROM embeddings e
			LEFT JOIN memories_index mi ON mi.id = e.item_id
			LEFT JOIN notes_index ni ON ni.id = e.item_id
			WHERE e.model = ?`
		if len(kinds) > 0 {
			sqlStr += ` AND e.kind IN (` + placeholders(len(kinds)) + `)`
			for _, k := range kinds {
				args = append(args, k)
			}
		}
		sqlStr += ` AND COALESCE(mi.project, ni.project) IN (` + placeholders(len(projects)) + `)`
		for _, p := range projects {
			args = append(args, p)
		}
	}

	rows, err := db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("store.CosineSearch: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var hits []SearchHit
	for rows.Next() {
		var (
			itemID, kind string
			blob         []byte
		)
		if err := rows.Scan(&itemID, &kind, &blob); err != nil {
			return nil, fmt.Errorf("store.CosineSearch: scan: %w", err)
		}
		vec := DecodeVector(blob)
		if len(vec) != len(query) {
			continue // corrupt row or a different model's dimensionality
		}
		hits = append(hits, SearchHit{ItemID: itemID, Kind: kind, Score: Cosine(query, vec)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store.CosineSearch: %w", err)
	}

	// Highest score first; ties broken by item_id for deterministic output.
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].ItemID < hits[j].ItemID
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// placeholders returns "?, ?, ..." with n placeholders for an IN clause.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}
