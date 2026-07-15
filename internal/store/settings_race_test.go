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

// familyRace runs fn once per index across n goroutines released together, and
// fails the test if any of them errors. It is the shared harness for the family
// mutator lost-update regressions below.
func familyRace(t *testing.T, n int, fn func(i int) error) {
	t.Helper()
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- fn(i)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

// TestAddFamilyMembers_ConcurrentWritersLoseNothing is the regression test for
// the AddFamilyMembers read-modify-write race: the mutator used to read the
// families map, mutate it, and write it back outside any transaction, so two
// callers growing different families at once both read the same snapshot and the
// second write-back dropped the first caller's family. The mutation now runs
// inside a single transaction (serialized by the one-connection pool), so every
// concurrent family must survive. Run with -race.
func TestAddFamilyMembers_ConcurrentWritersLoseNothing(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	const n = 32
	familyRace(t, n, func(i int) error {
		_, err := AddFamilyMembers(ctx, db, fmt.Sprintf("fam-%02d", i), []string{fmt.Sprintf("slug-%02d", i)})
		return err
	})

	fams, err := ProjectFamilies(ctx, db)
	require.NoError(t, err)
	require.Len(t, fams, n, "a concurrent AddFamilyMembers must never clobber another writer's family")
	for i := range n {
		require.Equal(t, []string{fmt.Sprintf("slug-%02d", i)}, fams[fmt.Sprintf("fam-%02d", i)])
	}
}

// TestAddFamilyMembers_ConcurrentSameFamily is the same race within one family:
// concurrent callers each adding a distinct member must all land, since the
// union is read and written under one transaction.
func TestAddFamilyMembers_ConcurrentSameFamily(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	const n = 32
	familyRace(t, n, func(i int) error {
		_, err := AddFamilyMembers(ctx, db, "shared", []string{fmt.Sprintf("slug-%02d", i)})
		return err
	})

	fams, err := ProjectFamilies(ctx, db)
	require.NoError(t, err)
	require.Len(t, fams["shared"], n, "every concurrently added member must survive")
	for i := range n {
		require.Contains(t, fams["shared"], fmt.Sprintf("slug-%02d", i))
	}
}

// TestRemoveFamilyMembers_ConcurrentWritersLoseNothing is the mirror regression
// for RemoveFamilyMembers: concurrent removals from different families used to
// write back a stale snapshot, resurrecting a member another caller had just
// dropped. Run with -race.
func TestRemoveFamilyMembers_ConcurrentWritersLoseNothing(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Seed every family with one member to drop and one to keep, so a lost update
	// shows up as a resurrected member rather than a vanished family.
	const n = 32
	seed := map[string][]string{}
	for i := range n {
		seed[fmt.Sprintf("fam-%02d", i)] = []string{fmt.Sprintf("drop-%02d", i), fmt.Sprintf("keep-%02d", i)}
	}
	require.NoError(t, SetProjectFamilies(ctx, db, seed))

	familyRace(t, n, func(i int) error {
		_, err := RemoveFamilyMembers(ctx, db, fmt.Sprintf("fam-%02d", i), []string{fmt.Sprintf("drop-%02d", i)})
		return err
	})

	fams, err := ProjectFamilies(ctx, db)
	require.NoError(t, err)
	require.Len(t, fams, n, "removing a member must not drop another writer's family")
	for i := range n {
		require.Equal(t, []string{fmt.Sprintf("keep-%02d", i)}, fams[fmt.Sprintf("fam-%02d", i)],
			"a concurrent RemoveFamilyMembers must not resurrect another writer's dropped member")
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
