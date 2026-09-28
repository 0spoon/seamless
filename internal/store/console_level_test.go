package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
)

func TestConsoleLevelOverride(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := config.Defaults().Console.Level

	// No row: the base passes through, nothing is overridden, and a fresh
	// installation has not seen the welcome card.
	level, overridden, source, welcomed, err := ConsoleLevel(ctx, db, base)
	require.NoError(t, err)
	require.Equal(t, "basic", level)
	require.False(t, overridden)
	require.Empty(t, source)
	require.False(t, welcomed)

	// A chosen level layers over the base, round-trips with its source, and
	// answers the welcome card.
	require.NoError(t, SetConsoleLevel(ctx, db, "standard", ConsoleLevelChosen))
	level, overridden, source, welcomed, err = ConsoleLevel(ctx, db, base)
	require.NoError(t, err)
	require.Equal(t, "standard", level)
	require.True(t, overridden)
	require.Equal(t, ConsoleLevelChosen, source)
	require.True(t, welcomed, "choosing a level anywhere answers the welcome card")

	// The row wins in both directions: a stored basic beats a file/env advanced.
	require.NoError(t, SetConsoleLevel(ctx, db, "basic", ConsoleLevelChosen))
	level, _, _, _, err = ConsoleLevel(ctx, db, "advanced")
	require.NoError(t, err)
	require.Equal(t, "basic", level, "the stored override wins over the file/env base")

	// Clearing reverts to the base but keeps the welcome: a reset is not a
	// request to be asked again. Clearing twice is a no-op.
	require.NoError(t, ClearConsoleLevel(ctx, db))
	require.NoError(t, ClearConsoleLevel(ctx, db))
	level, overridden, source, welcomed, err = ConsoleLevel(ctx, db, "advanced")
	require.NoError(t, err)
	require.Equal(t, "advanced", level)
	require.False(t, overridden)
	require.Empty(t, source)
	require.True(t, welcomed, "a reset keeps the welcome flag")
}

func TestConsoleLevel_SetRejectsUnknownValues(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	err := SetConsoleLevel(ctx, db, "expert", ConsoleLevelChosen)
	require.ErrorContains(t, err, `invalid level "expert"`)
	require.ErrorContains(t, err, "basic, standard, advanced")

	err = SetConsoleLevel(ctx, db, "advanced", "guessed")
	require.ErrorContains(t, err, `invalid source "guessed"`)

	_, found, err := GetSetting(ctx, db, SettingConsoleLevel)
	require.NoError(t, err)
	require.False(t, found, "a refused value must never reach the row")
}

func TestConsoleLevel_WelcomeWithoutChoosing(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Dismissing the card on a fresh install records the welcome WITHOUT a
	// level, so the effective level keeps following file/env.
	require.NoError(t, MarkConsoleWelcomed(ctx, db))
	level, overridden, source, welcomed, err := ConsoleLevel(ctx, db, "standard")
	require.NoError(t, err)
	require.Equal(t, "standard", level)
	require.False(t, overridden, "a welcome-only row overrides nothing")
	require.Empty(t, source)
	require.True(t, welcomed)

	// Marking the welcome later leaves a stored level (and its source) alone.
	require.NoError(t, SetSetting(ctx, db, SettingConsoleLevel,
		`{"level":"advanced","source":"seeded","welcomed":false}`))
	require.NoError(t, MarkConsoleWelcomed(ctx, db))
	level, overridden, source, welcomed, err = ConsoleLevel(ctx, db, "basic")
	require.NoError(t, err)
	require.Equal(t, "advanced", level)
	require.True(t, overridden)
	require.Equal(t, ConsoleLevelSeeded, source, "the welcome must not re-credit a seeded level to the owner")
	require.True(t, welcomed)

	// A reset of an unwelcomed row deletes it outright.
	require.NoError(t, SetSetting(ctx, db, SettingConsoleLevel,
		`{"level":"advanced","source":"seeded","welcomed":false}`))
	require.NoError(t, ClearConsoleLevel(ctx, db))
	_, found, err := GetSetting(ctx, db, SettingConsoleLevel)
	require.NoError(t, err)
	require.False(t, found)
}

func TestConsoleLevel_PartialAndCorruptRows(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// A partial row (level only) is a valid override that has not welcomed.
	require.NoError(t, SetSetting(ctx, db, SettingConsoleLevel, `{"level":"standard"}`))
	level, overridden, source, welcomed, err := ConsoleLevel(ctx, db, "basic")
	require.NoError(t, err)
	require.Equal(t, "standard", level)
	require.True(t, overridden)
	require.Empty(t, source)
	require.False(t, welcomed)

	// A corrupt row errors and reports the base, so the caller can degrade
	// rather than render a level nobody chose.
	for _, raw := range []string{
		`{not json`,
		`{"level":"expert"}`,
		`{"level":"basic","source":"guessed"}`,
	} {
		require.NoError(t, SetSetting(ctx, db, SettingConsoleLevel, raw))
		level, overridden, _, _, err = ConsoleLevel(ctx, db, "advanced")
		require.Error(t, err, "row %s must be reported, never silently decoded", raw)
		require.Equal(t, "advanced", level, "a corrupt row reports the base")
		require.False(t, overridden)

		// A mutation that keeps the rest of the row refuses to guess at it...
		require.Error(t, MarkConsoleWelcomed(ctx, db))
	}

	// ...but reset is the way out of any state, so it clears a corrupt row.
	require.NoError(t, ClearConsoleLevel(ctx, db))
	level, overridden, _, _, err = ConsoleLevel(ctx, db, "advanced")
	require.NoError(t, err)
	require.Equal(t, "advanced", level)
	require.False(t, overridden)
}

// TestConsoleLevelStoredShapeMatchesMigration pins the JSON the grandfather
// migration writes to the shape the Go writer produces: the migration is raw SQL
// and cannot see the struct tags.
func TestConsoleLevelStoredShapeMatchesMigration(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	require.NoError(t, writeConsoleLevelRow(ctx, db,
		consoleLevelRow{Level: "advanced", Source: ConsoleLevelSeeded, Welcomed: false}))
	raw, found, err := GetSetting(ctx, db, SettingConsoleLevel)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, `{"level":"advanced","source":"seeded","welcomed":false}`, raw,
		"migration 026 seeds exactly this literal")
}

// TestMigration026_GrandfathersExistingInstallations upgrades a database that
// already recorded sessions and verifies the seeded row keeps every screen --
// the default is basic, and an upgrade must never hide a screen in use.
func TestMigration026_GrandfathersExistingInstallations(t *testing.T) {
	for _, tc := range []struct {
		name         string
		seedSession  bool
		existing     string // a console_level row present before the migration
		wantLevel    string
		wantOverride bool
		wantSource   string
		wantWelcomed bool
	}{
		{
			name: "sessions present seeds advanced", seedSession: true,
			wantLevel: "advanced", wantOverride: true, wantSource: ConsoleLevelSeeded,
			wantWelcomed: false,
		},
		{name: "fresh database seeds nothing", wantLevel: "basic"},
		{
			name: "existing row is never overwritten", seedSession: true,
			existing:  `{"level":"standard","source":"chosen","welcomed":true}`,
			wantLevel: "standard", wantOverride: true, wantSource: ConsoleLevelChosen,
			wantWelcomed: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openPartialDB(t, 26)
			ctx := context.Background()

			if tc.seedSession {
				_, err := db.Exec(`
					INSERT INTO sessions (id, name, project_slug, status, findings, created_at, updated_at)
					VALUES ('01SESS', 'cc/old', 'seam', 'completed', '', '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z')`)
				require.NoError(t, err)
			}
			if tc.existing != "" {
				require.NoError(t, SetSetting(ctx, db, SettingConsoleLevel, tc.existing))
			}

			require.NoError(t, migrate(db, migrationList()))

			level, overridden, source, welcomed, err := ConsoleLevel(ctx, db, config.Defaults().Console.Level)
			require.NoError(t, err)
			require.Equal(t, tc.wantLevel, level)
			require.Equal(t, tc.wantOverride, overridden)
			require.Equal(t, tc.wantSource, source)
			require.Equal(t, tc.wantWelcomed, welcomed)
		})
	}
}

// TestMigration026_IsOneTime proves the seed does not come back after the owner
// resets the level: the version is recorded, so a later start re-runs nothing.
func TestMigration026_IsOneTime(t *testing.T) {
	db := openPartialDB(t, 26)
	ctx := context.Background()

	_, err := db.Exec(`
		INSERT INTO sessions (id, name, project_slug, status, findings, created_at, updated_at)
		VALUES ('01SESS', 'cc/old', 'seam', 'completed', '', '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z')`)
	require.NoError(t, err)
	require.NoError(t, migrate(db, migrationList()))

	// The owner resets to file/env in the console.
	require.NoError(t, ClearConsoleLevel(ctx, db))
	require.NoError(t, migrate(db, migrationList()))
	level, overridden, _, _, err := ConsoleLevel(ctx, db, "basic")
	require.NoError(t, err)
	require.Equal(t, "basic", level)
	require.False(t, overridden)
}
