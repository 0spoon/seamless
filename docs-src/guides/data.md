---
title: Import, back up & restore
description: Putting ~/.seamless in git, what deleting seam.db actually costs, restoring by rebuilding the index, and moving to a new machine.
---

Because durable knowledge is markdown files, backup and restore are boring - and
that is the feature. This page is mostly about knowing which half of
`~/.seamless` is precious and which half regenerates.

## What is precious, and what is not

```text
~/.seamless/
  memory/{project|_global}/{name}.md   PRECIOUS -- source of truth
  notes/{project|_global}/{slug}.md    PRECIOUS -- source of truth
  seam.db                              MIXED (see below)
```

`seam.db` holds two different kinds of thing, and conflating them is what makes
people either over-protect it or under-protect it:

| In `seam.db` | If it were lost |
|---|---|
| FTS index, embeddings | **Rebuilt automatically** from the files |
| Sessions, tasks, trials, events, telemetry, briefing overrides | **Gone** - there is no file to rebuild them from |

So: **deleting `seam.db` costs you the record of what happened, not the knowledge
of what is true.** Every memory and note survives, because they are files.

## Put it in git

The strongest backup is the one you already know how to use:

```bash
cd ~/.seamless
git init
printf 'seam.db\nseam.db-wal\nseam.db-shm\n' > .gitignore
git add . && git commit -m "seamless: initial"
```

Ignoring the database is deliberate. It is a binary that changes constantly, it
does not diff usefully, and its contents are either rebuildable or high-churn
state that a nightly commit would capture uselessly. What you want in git is the
knowledge - and that diffs beautifully, because it is markdown.

Commit periodically (a cron job or a `launchd` timer is plenty). The payoff is
that `git log` over your memory directory is a real history of what your agents
learned and when they changed their minds.

If you would rather keep the tasks and sessions too, back up `seam.db` with
SQLite's own tooling rather than copying the file while the daemon is running:

```bash
sqlite3 ~/.seamless/seam.db ".backup '/tmp/seam-backup.db'"
```

A plain `cp` of a WAL-mode database under a live writer can capture a torn state.
`.backup` will not.

## Restore

Restoring is: put the files back, start the daemon.

```bash
# files back in place
cp -R backup/memory backup/notes ~/.seamless/
make run
```

Startup reconciliation walks the tree, notices which files the index does not
know about (or whose content hash changed), and indexes them. A file watcher
keeps it in sync from then on. You do not run a reindex command, because there
isn't one to forget.

To force a full rebuild, stop the daemon, delete `seam.db`, and start it again.
You lose sessions, tasks, trials, and events; you lose no knowledge.

## Import from Seam v1

```bash
seamlessd import --help
```

The importer brings a v1 store's memories, sessions, and tool-call events into
`seam.db`. It is **idempotent**: running it twice does not double anything, so a
partial import is safe to re-run.

## Hand-editing

Files are the source of truth, so editing them by hand is allowed and expected -
the watcher picks up your change and reindexes it.

Two rules:

1. **Never hand-stamp `invalid_at` or `superseded_by`.** Those are set by the
   supersede path, which also updates the indexes and the pointer between the two
   memories. Writing them by hand produces a file that says one thing and a
   database that believes another, and the lifecycle invariants (stamp once,
   point only at an active memory, never self-supersede) stop being true. Use
   `memory_write` with `supersedes` - see [Memory & notes](/concepts/memory/).
2. **Do not hand-edit `id`.** It is a ULID assigned once and referenced by
   `superseded_by` pointers elsewhere.

Everything else - the body, the description, tags, the kind - is yours to edit in
any text editor.

## Moving machines

```bash
# on the old machine
cd ~/.seamless && git push          # or: tar czf seamless.tgz memory notes

# on the new one
git clone <remote> ~/.seamless      # or untar
cp seamless.yaml.example seamless.yaml   # NEW key: do not reuse the old one
openssl rand -hex 32
make install
make doctor
```

The index rebuilds itself on first start. Generate a fresh `mcp.api_key` rather
than copying the old config: the key is the only credential, and a machine
migration is a good moment not to spread it around. See
[Install & deploy](/install/).
