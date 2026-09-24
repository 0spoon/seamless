// Package archive reads and writes Seamless instance archives: one gzipped tar
// carrying the markdown corpus, a consistent snapshot of seam.db, and a
// manifest describing what is inside.
//
// The archive is the whole instance minus its identity. Config and the MCP API
// key are never included -- a restored instance gets a new key and its own
// config, so an archive can be copied around without carrying a credential.
//
// Layout, in tar order:
//
//	manifest.json                     always FIRST, so a reader can refuse an
//	                                  archive before extracting a single byte
//	seam.db                           absent when exported with NoDB
//	memory/<project|_global>/<name>.md
//	notes/<project|_global>/<slug>.md
//
// Every entry is a regular file with mode 0600 and uid/gid zeroed: an archive
// restored as another user must not carry the exporting machine's ownership,
// and the corpus is owner-only on disk for the same reason the data dir is.
//
// The package imports core, files, store, and validate. It must NOT import
// internal/config: an archive is described by its manifest and its data dir
// argument, never by the config of whatever process happens to be holding it,
// which is what lets an export run against a data dir the caller names and an
// import target a directory that has no config yet.
package archive

import (
	"errors"
	"time"
)

// FormatVersion is the archive container version. It describes the tar layout
// and manifest shape, NOT the database schema: schema_version travels
// separately in the manifest because a v1 archive is written by every seamlessd
// regardless of how far its migrations have run.
const FormatVersion = 1

// The fixed entry names inside an archive. They are constants rather than
// literals at each site because the writer and the reader have to agree
// exactly, and a transcribed name drifts in silence (AGENTS.md).
const (
	// ManifestName is the first entry of every archive.
	ManifestName = "manifest.json"
	// DBName is the snapshot of seam.db, absent from a NoDB export.
	DBName = "seam.db"
	// MemoryTree and NotesTree are the two markdown trees carried verbatim.
	MemoryTree = "memory"
	NotesTree  = "notes"
)

// Sentinels. Each one names a distinct refusal, so callers can tell "this file
// is not one of ours" from "this file is ours and I am too old to read it"
// without matching on message text.
var (
	// ErrSchemaTooNew reports an archive whose schema_version exceeds this
	// binary's store.LatestSchemaVersion. Migrating forward cannot help: the
	// migrations that produced the snapshot are not in this build, so the only
	// remedy is a newer seamlessd.
	ErrSchemaTooNew = errors.New("archive was made by a newer seamlessd")

	// ErrUnsafeEntry reports a tar entry that must never be written to disk --
	// a symlink or hardlink, a non-regular file, a path escaping the
	// destination, or a name outside the archive layout. It also covers the
	// export side of the same rule: a memory/ or notes/ tree root that is not a
	// real directory is refused rather than silently exported as empty.
	ErrUnsafeEntry = errors.New("unsafe archive entry")

	// ErrNotArchive reports a file that is not a Seamless archive: not a
	// gzipped tar, or a tar whose first entry is not manifest.json.
	ErrNotArchive = errors.New("not a seamless archive")

	// ErrDaemonRunning reports that a daemon is answering on the target
	// instance's address. A fresh restore replaces seam.db underneath it, so it
	// refuses rather than corrupting a live instance.
	ErrDaemonRunning = errors.New("seamlessd is running against this data dir")
)

// Counts is the manifest's inventory: what a reader should expect to find, so a
// truncated or hand-edited archive is detectable before it is trusted.
type Counts struct {
	// MemoryFiles and NoteFiles count the .md entries in each tree.
	MemoryFiles int `json:"memory_files"`
	NoteFiles   int `json:"note_files"`

	// Tables maps table name to row count in the seam.db snapshot, read from
	// the snapshot after it verified rather than from the live database, so the
	// numbers describe the bytes actually in the archive. Empty for a NoDB
	// export. Enumerated from sqlite_master rather than a hand-written list, so
	// a table added by a later migration appears without anyone remembering to
	// add it here.
	Tables map[string]int `json:"tables,omitempty"`
}

// Manifest is manifest.json, the first entry of every archive.
type Manifest struct {
	// FormatVersion is the container version (FormatVersion).
	FormatVersion int `json:"format_version"`

	// SchemaVersion is the migration version of the snapshot, 0 for a NoDB
	// export. A value above the reader's store.LatestSchemaVersion is
	// ErrSchemaTooNew; anything at or below it migrates forward on import.
	SchemaVersion int `json:"schema_version"`

	// SeamlessdVersion is the build that wrote the archive, supplied by the
	// caller: the version is linked into the binary, and this package sits
	// below cmd/.
	SeamlessdVersion string `json:"seamlessd_version"`

	CreatedAt  time.Time `json:"created_at"`
	SourceHost string    `json:"source_host"`

	// IncludesDB is false for a knowledge-only (NoDB) export, where the tar
	// carries the two markdown trees and nothing else.
	IncludesDB bool `json:"includes_db"`

	// EmbeddingModels is DISTINCT model FROM embeddings in the snapshot. The
	// importer compares it with the destination's configured embedder: vectors
	// from another model are not comparable, so a mismatch is the signal to
	// re-embed rather than to trust the imported rows.
	EmbeddingModels []string `json:"embedding_models,omitempty"`

	Counts Counts `json:"counts"`
}
