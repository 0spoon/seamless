package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// Migration 025 on a fresh database: the host column and the repo_map table
// exist with the shape every query below assumes.
func TestMigration025_SessionHostAndRepoMap(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	var host string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT host FROM sessions WHERE 1 = 0 UNION ALL SELECT ''`).Scan(&host),
		"sessions.host must exist after migration 025")

	_, err := db.ExecContext(ctx, `
		INSERT INTO repo_map (host, path, slug, origin, created_at)
		VALUES ('h1', '/r', 'p', '', ?)`, core.FormatTime(time.Now().UTC()))
	require.NoError(t, err)

	// The primary key is (host, path): the same path on another host is another
	// row, the same path on the same host is a conflict.
	_, err = db.ExecContext(ctx, `
		INSERT INTO repo_map (host, path, slug, origin, created_at)
		VALUES ('h2', '/r', 'other', '', ?)`, core.FormatTime(time.Now().UTC()))
	require.NoError(t, err, "the same path on a second host is a distinct mapping")
	_, err = db.ExecContext(ctx, `
		INSERT INTO repo_map (host, path, slug, origin, created_at)
		VALUES ('h1', '/r', 'dup', '', ?)`, core.FormatTime(time.Now().UTC()))
	require.Error(t, err, "(host, path) must be unique")
}

// AdoptLocalHost is what an existing install runs into on its first start after
// upgrading: sessions and the JSON repo map predate the host column entirely.
// Running it twice must change nothing the first run did not.
func TestAdoptLocalHost_BackfillsAndIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	id, err := core.NewID()
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, CreateSession(ctx, db, core.Session{
		ID: id, Name: "cc/legacy", Status: core.SessionActive, Ambient: true,
		CWD: "/work/app", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, SetSetting(ctx, db, SettingRepoProjectMap, `{"/work/app":"app"}`))

	require.NoError(t, AdoptLocalHost(ctx, db, "Alpha"))

	sess, ok, err := SessionByID(ctx, db, id)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "Alpha", sess.Host, "a session written before host scoping becomes this machine's")

	rows, err := RepoMapRows(ctx, db)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "Alpha", rows[0].Host)
	require.Equal(t, "/work/app", rows[0].Path)
	require.Equal(t, "app", rows[0].Slug)

	// The mirror still reads as the local view, so the console and doctor are
	// unaffected by the move to the table.
	mirror, err := RepoProjectMap(ctx, db)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"/work/app": "app"}, mirror)

	require.NoError(t, AdoptLocalHost(ctx, db, "Alpha"))
	again, err := RepoMapRows(ctx, db)
	require.NoError(t, err)
	require.Equal(t, rows, again, "adoption must be idempotent")
}

// A mapping recorded before the daemon ever ran (a seeder, `map-repo` on a fresh
// data dir) lands in the unnamed bucket; adoption re-homes it rather than
// leaving it in a bucket no resolver asks for.
func TestAdoptLocalHost_ReHomesTheUnnamedBucket(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	require.NoError(t, AddRepoMapping(ctx, db, "/seeded/repo", "seeded"))
	rows, err := RepoMapRows(ctx, db)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Empty(t, rows[0].Host, "no daemon has named this machine yet")

	require.NoError(t, AdoptLocalHost(ctx, db, "alpha"))
	rows, err = RepoMapRows(ctx, db)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "alpha", rows[0].Host)

	slug, err := ResolveProjectForCWD(ctx, db, "alpha", "/seeded/repo/pkg")
	require.NoError(t, err)
	require.Equal(t, "seeded", slug)
}

// After adoption, AddRepoMapping (the `seamlessd map-repo` path) must land in
// the SAME bucket the resolvers read, or an owner override would take effect
// only after a restart.
func TestAddRepoMapping_LandsOnTheAdoptedHost(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	require.NoError(t, AdoptLocalHost(ctx, db, "alpha"))
	require.NoError(t, AddRepoMapping(ctx, db, "/work/override", "chosen"))

	slug, err := ResolveProjectForCWD(ctx, db, "alpha", "/work/override/sub")
	require.NoError(t, err)
	require.Equal(t, "chosen", slug)

	mirror, err := RepoProjectMap(ctx, db)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"/work/override": "chosen"}, mirror)
}

// A remote agent that sends its roots is placed without the daemon ever touching
// its own disk, and the row it writes is scoped to ITS host.
func TestRegisterProjectForCWD_RemoteWithRoots(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	require.NoError(t, AdoptLocalHost(ctx, db, "alpha"))

	slug, moved, err := RegisterProjectForCWD(ctx, db, CWDIdentity{
		Host: "beta", CWD: `C:\repos\myapp\internal`, RepoRoot: `C:\repos\myapp`,
		MainRoot: `C:\repos\myapp`, Origin: "git@github.com:acme/myapp.git",
	}, "alpha")
	require.NoError(t, err)
	require.Nil(t, moved)
	require.Equal(t, "myapp", slug, "a Windows root yields the same slug on a POSIX daemon")

	rows, err := RepoMapRows(ctx, db)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "beta", rows[0].Host)
	require.Equal(t, `C:\repos\myapp`, rows[0].Path)

	// The local host's map is untouched -- including its mirror, which the
	// console still reads.
	local, err := ResolveProjectForCWD(ctx, db, "alpha", `C:\repos\myapp\internal`)
	require.NoError(t, err)
	require.Empty(t, local, "a remote mapping must not resolve for the local host")
	mirror, err := RepoProjectMap(ctx, db)
	require.NoError(t, err)
	require.Empty(t, mirror, "a remote registration must not touch the local mirror")

	// The remote host resolves its own subdirectories, backslashes and all.
	got, err := ResolveProjectForCWD(ctx, db, "beta", `C:\repos\myapp\internal\store`)
	require.NoError(t, err)
	require.Equal(t, "myapp", got)
}

// The whole point of Phase 1: a remote session with no roots is refused rather
// than resolved against the daemon's own filesystem, and nothing is written.
func TestRegisterProjectForCWD_RemoteWithoutRootsWritesNothing(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	require.NoError(t, AdoptLocalHost(ctx, db, "alpha"))

	// A real repo on the DAEMON's disk at the very path the remote client names:
	// the pre-host-scoping code would have walked it and mapped the remote agent
	// to this machine's repo.
	root := filepath.Join(t.TempDir(), "decoy")
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))

	slug, moved, err := RegisterProjectForCWD(ctx, db,
		CWDIdentity{Host: "beta", CWD: root}, "alpha")
	require.ErrorIs(t, err, ErrRemoteRootUnknown)
	require.Empty(t, slug)
	require.Nil(t, moved)

	rows, err := RepoMapRows(ctx, db)
	require.NoError(t, err)
	require.Empty(t, rows, "a refused registration must write nothing")
	require.Empty(t, ListProjectsSlugs(t, db))
}

// Cross-host adoption: the same repository checked out on a second machine joins
// the existing project when the origins agree (or either is unknown), and mints
// its own when two KNOWN origins disagree.
func TestRegisterProjectForCWD_CrossHostAdoption(t *testing.T) {
	const origin = "https://github.com/acme/backend.git"
	for _, tc := range []struct {
		name         string
		firstOrigin  string
		secondOrigin string
		want         string
	}{
		{"matching origins adopt", origin, "git@github.com:acme/backend.git", "backend"},
		{"unknown on the new side adopts", origin, "", "backend"},
		{"unknown on the owning side adopts", "", origin, "backend"},
		{"both unknown adopts", "", "", "backend"},
		{"known and different mints", origin, "https://github.com/other/backend.git", "backend-2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			ctx := context.Background()
			require.NoError(t, AdoptLocalHost(ctx, db, "alpha"))

			slug, _, err := RegisterProjectForCWD(ctx, db, CWDIdentity{
				Host: "beta", CWD: "/srv/backend", RepoRoot: "/srv/backend", Origin: tc.firstOrigin,
			}, "alpha")
			require.NoError(t, err)
			require.Equal(t, "backend", slug)

			slug, _, err = RegisterProjectForCWD(ctx, db, CWDIdentity{
				Host: "gamma", CWD: "/home/me/backend", RepoRoot: "/home/me/backend", Origin: tc.secondOrigin,
			}, "alpha")
			require.NoError(t, err)
			require.Equal(t, tc.want, slug)
		})
	}
}

// Two different repos that share a directory name on the SAME host keep today's
// rule: mint, never merge. (Cross-host adoption must not loosen this.)
func TestRegisterProjectForCWD_SameHostCollisionStillMints(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	require.NoError(t, AdoptLocalHost(ctx, db, "alpha"))

	const origin = "https://github.com/acme/backend.git"
	slug, _, err := RegisterProjectForCWD(ctx, db, CWDIdentity{
		Host: "beta", CWD: "/a/backend", RepoRoot: "/a/backend", Origin: origin,
	}, "alpha")
	require.NoError(t, err)
	require.Equal(t, "backend", slug)

	slug, _, err = RegisterProjectForCWD(ctx, db, CWDIdentity{
		Host: "beta", CWD: "/b/backend", RepoRoot: "/b/backend", Origin: origin,
	}, "alpha")
	require.NoError(t, err)
	require.Equal(t, "backend-2", slug, "same host, two directories: two projects")
}

// The remap hazard this phase exists to close: a remote host's mapped path is
// always "missing" from the daemon's disk, and must never be read as a moved
// repo.
func TestRegisterProjectForCWD_DeadOwnerHealIgnoresRemoteRows(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	require.NoError(t, AdoptLocalHost(ctx, db, "alpha"))

	// beta maps a path that does not exist on the daemon's disk.
	_, _, err := RegisterProjectForCWD(ctx, db, CWDIdentity{
		Host: "beta", CWD: "/remote/backend", RepoRoot: "/remote/backend",
		Origin: "https://github.com/acme/backend.git",
	}, "alpha")
	require.NoError(t, err)

	// A LOCAL repo of the same name now registers. Its origin differs, so it is a
	// different repository: it must mint, and beta's row must survive untouched.
	local := filepath.Join(t.TempDir(), "backend")
	require.NoError(t, os.MkdirAll(filepath.Join(local, ".git"), 0o755))
	slug, moved, err := RegisterProjectForCWD(ctx, db, CWDIdentity{
		Host: "alpha", CWD: local, RepoRoot: local, Origin: "https://github.com/other/backend.git",
	}, "alpha")
	require.NoError(t, err)
	require.Nil(t, moved, "a remote row is never evidence that a repo moved")
	require.Equal(t, "backend-2", slug)

	got, err := ResolveProjectForCWD(ctx, db, "beta", "/remote/backend")
	require.NoError(t, err)
	require.Equal(t, "backend", got, "the remote mapping must be left alone")
}

// RepoRootsForProject is what the gardener opens on disk, so it must return one
// host's paths and not every host's.
func TestRepoRootsForProject_IsHostScoped(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	require.NoError(t, AdoptLocalHost(ctx, db, "alpha"))
	require.NoError(t, AddRepoMapping(ctx, db, "/local/app", "app"))
	_, _, err := RegisterProjectForCWD(ctx, db, CWDIdentity{
		Host: "beta", CWD: "/remote/app", RepoRoot: "/remote/app",
	}, "alpha")
	require.NoError(t, err)

	roots, err := RepoRootsForProject(ctx, db, "alpha")
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"app": {"/local/app"}}, roots)

	roots, err = RepoRootsForProject(ctx, db, "beta")
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"app": {"/remote/app"}}, roots)
}

// Two agents in the same absolute path on two machines are two different agents.
func TestActiveAmbientByCWD_SameCWDOnTwoHosts(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	mk := func(name, host string) {
		id, err := core.NewID()
		require.NoError(t, err)
		require.NoError(t, CreateSession(ctx, db, core.Session{
			ID: id, Name: name, Status: core.SessionActive, Ambient: true,
			CWD: "/work/app", Host: host, CreatedAt: now, UpdatedAt: now,
		}))
	}
	mk("cc/alpha", "alpha")
	mk("cc/beta", "beta")

	got, err := ActiveAmbientByCWD(ctx, db, "alpha", "/work/app")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "cc/alpha", got[0].Name)

	got, err = ActiveAmbientByCWD(ctx, db, "beta", "/work/app")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "cc/beta", got[0].Name)

	got, err = ActiveAmbientByCWD(ctx, db, "gamma", "/work/app")
	require.NoError(t, err)
	require.Empty(t, got, "a third machine shares neither session")
}

// The host filter on the project-scoped ambient lookups is optional, and ""
// means "every host" rather than "the unnamed one" -- that is what lets a caller
// widen its search after finding nothing on its own machine.
func TestActiveAmbientProjects_HostFilterIsOptional(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	mk := func(name, host, project string) {
		id, err := core.NewID()
		require.NoError(t, err)
		require.NoError(t, CreateSession(ctx, db, core.Session{
			ID: id, Name: name, ProjectSlug: project, Status: core.SessionActive,
			Ambient: true, Host: host, CreatedAt: now, UpdatedAt: now,
		}))
	}
	mk("cc/a", "alpha", "app")
	mk("cc/b", "beta", "other")

	projects, err := ActiveAmbientProjects(ctx, db, "alpha", time.Hour)
	require.NoError(t, err)
	require.Equal(t, []string{"app"}, projects)

	projects, err = ActiveAmbientProjects(ctx, db, "", time.Hour)
	require.NoError(t, err)
	require.Len(t, projects, 2, `"" is no filter at all`)

	sessions, err := ActiveAmbientSessionsForProject(ctx, db, "beta", "other", time.Hour)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	sessions, err = ActiveAmbientSessionsForProject(ctx, db, "alpha", "other", time.Hour)
	require.NoError(t, err)
	require.Empty(t, sessions)
}

// normalizeOrigin decides whether two checkouts are the same repository, so the
// forms git actually writes must all reduce to one value.
func TestNormalizeOrigin(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"https://github.com/acme/Repo.git", "github.com/acme/repo"},
		{"git@github.com:acme/repo.git", "github.com/acme/repo"},
		{"ssh://git@github.com/acme/repo", "github.com/acme/repo"},
		{"https://github.com/acme/repo/", "github.com/acme/repo"},
		{"ssh://git@host:2222/acme/repo.git", "host:2222/acme/repo"},
	} {
		require.Equal(t, tc.want, normalizeOrigin(tc.in), tc.in)
	}
}

// Prefix matching has to work for a client whose separator is not the daemon's.
func TestPathHasPrefix_BothSeparators(t *testing.T) {
	require.True(t, pathHasPrefix("/a/b/c", "/a/b"))
	require.True(t, pathHasPrefix(`C:\a\b\c`, `C:\a\b`))
	require.True(t, pathHasPrefix("/a/b", "/a/b"))
	require.False(t, pathHasPrefix("/a/bc", "/a/b"))
	require.False(t, pathHasPrefix(`C:\a\bc`, `C:\a\b`))
}
