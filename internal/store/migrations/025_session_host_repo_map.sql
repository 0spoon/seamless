-- Host-scoped identity: which machine a session and a repo mapping came from.
--
-- Until now every session and every repo->project mapping was implicitly the
-- daemon's own machine, because the daemon and its clients were always the same
-- machine. Once several devices share one daemon that assumption becomes a
-- corruption hazard rather than a simplification: deadOwnerPaths reads "this
-- mapped path is missing on my disk" as a moved repo and re-points the project,
-- so a remote client whose paths the daemon cannot see would silently remap
-- every project it touched. Stamping the host is what lets that heal stay local.
--
-- Existing rows get host = '' -- the machine that has not named itself. The
-- daemon adopts them for its own hostname at startup (store.AdoptLocalHost), so
-- the blank is a migration state rather than a value anything keeps.
ALTER TABLE sessions ADD COLUMN host TEXT NOT NULL DEFAULT '';

-- The ambient-session lookups all key on (host, cwd) now: "which agent is
-- working in this directory ON THIS MACHINE". Partial, because only active
-- ambient rows are ever asked for.
CREATE INDEX idx_sessions_active_ambient_host_cwd
    ON sessions(host, cwd)
 WHERE ambient = 1 AND status = 'active';

-- A table rather than a host:path key inside the repo_project_map JSON object:
-- Windows paths contain ':' (C:\repos\myapp), so a composite string key cannot
-- be split back apart, and every query wants WHERE host = ? anyway.
--
-- origin is the repository's origin remote URL, the one piece of repo identity
-- that survives being cloned onto another machine under a different path; '' is
-- "unknown", never "no remote of interest".
CREATE TABLE repo_map (
    host       TEXT NOT NULL,
    path       TEXT NOT NULL,
    slug       TEXT NOT NULL,
    origin     TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    PRIMARY KEY (host, path)
);

-- "Which paths, on which hosts, own this project" -- the cross-host adoption
-- check and RepoRootsForProject both start here.
CREATE INDEX idx_repo_map_slug ON repo_map(slug);
