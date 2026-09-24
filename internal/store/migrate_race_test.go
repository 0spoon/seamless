package store

import (
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMigrate_ConcurrentAppliersDoNotCollide pins the fix for the install-time
// race: `make install` restarts the daemon and then runs install-hooks, so two
// processes open the same existing database at once and both find the same
// migration pending. Before the fix each read the current version outside any
// transaction and began a DEFERRED one, so nothing serialized them -- the loser
// replayed a migration the winner had already committed and died on
// "duplicate column name: host".
//
// Separate sql.DB handles stand in for separate processes: each has its own
// pool, so these are genuinely distinct SQLite connections. The database is
// created and fully migrated first, exactly like the machine an install runs
// on; only the synthetic migration below is pending when the appliers start.
func TestMigrate_ConcurrentAppliersDoNotCollide(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "seam.db")

	seed, err := Open(dbPath)
	require.NoError(t, err)
	require.NoError(t, seed.Close())

	// One pending migration whose SQL is not idempotent, the same shape as the
	// v25 ALTER TABLE that exposed the race.
	pending := append(migrationList(), Migration{
		Version: LatestSchemaVersion() + 1,
		SQL:     "ALTER TABLE sessions ADD COLUMN race_probe TEXT",
	})

	const appliers = 4
	dbs := make([]*sql.DB, appliers)
	for i := range dbs {
		db, err := sql.Open("sqlite", dsn(dbPath))
		require.NoError(t, err)
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = db.Close() })
		dbs[i] = db
	}

	errs := make([]error, appliers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range appliers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = migrate(dbs[i], pending)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		require.NoErrorf(t, err, "applier %d", i)
	}

	got, err := SchemaVersion(dbs[0])
	require.NoError(t, err)
	require.Equal(t, LatestSchemaVersion()+1, got)

	// Exactly one row per migration: a concurrent applier must not record a
	// version the winner already recorded.
	var rows int
	require.NoError(t, dbs[0].QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&rows))
	require.Equal(t, LatestSchemaVersion()+1, rows)
}
