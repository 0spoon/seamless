package store

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAddRepoMapping_ConcurrentWritersLoseNothing is the regression test for the
// AddRepoMapping read-modify-write race: two agents registering different repos
// at the same time used to read the same map snapshot and the second write-back
// dropped the first agent's entry. The mutation now runs inside a single
// transaction (serialized by the one-connection pool), so every concurrent
// mapping must survive. Run with -race.
func TestAddRepoMapping_ConcurrentWritersLoseNothing(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	const n = 32
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- AddRepoMapping(ctx, db, fmt.Sprintf("/repo/r%02d", i), fmt.Sprintf("slug-%02d", i))
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	m, err := RepoProjectMap(ctx, db)
	require.NoError(t, err)
	require.Len(t, m, n, "a concurrent AddRepoMapping must never clobber another writer's entry")
	for i := range n {
		require.Equal(t, fmt.Sprintf("slug-%02d", i), m[fmt.Sprintf("/repo/r%02d", i)])
	}
}

// TestAddRepoMapping_ConcurrentSameKey pins the idempotent case under
// concurrency: many writers recording the exact same entry all succeed and the
// map ends with that single entry.
func TestAddRepoMapping_ConcurrentSameKey(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	const n = 16
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- AddRepoMapping(ctx, db, "/repo/same", "same-slug")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	m, err := RepoProjectMap(ctx, db)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"/repo/same": "same-slug"}, m)
}
