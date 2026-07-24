package core

import (
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
)

func TestNewID_ValidAndUnique(t *testing.T) {
	// NewID uses random (not monotonic) entropy, so two IDs minted in the same
	// millisecond are not ordered relative to each other -- only uniqueness and
	// parseability are guaranteed. Cross-millisecond ULIDs sort by time.
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		id, err := NewID()
		require.NoError(t, err)
		require.Len(t, id, 26)

		_, err = ulid.Parse(id)
		require.NoError(t, err, "NewID must produce a parseable ULID")

		_, dup := seen[id]
		require.False(t, dup, "NewID must not collide")
		seen[id] = struct{}{}
	}
}

func TestMemoryKind_Valid(t *testing.T) {
	require.True(t, KindConstraint.Valid())
	require.True(t, KindConvention.Valid())
	require.True(t, KindStage.Valid())
	require.False(t, MemoryKind("bogus").Valid())
	require.False(t, MemoryKind("").Valid())
	require.Len(t, MemoryKinds, 9)
}

func TestMemory_Active(t *testing.T) {
	m := Memory{Kind: KindGotcha, Name: "x"}
	require.True(t, m.Active())

	now := time.Now().UTC()
	m.InvalidAt = &now
	require.False(t, m.Active())
}

func TestTaskStatus(t *testing.T) {
	require.True(t, TaskOpen.Valid())
	require.True(t, TaskDropped.Valid())
	require.False(t, TaskStatus("paused").Valid())

	require.False(t, TaskOpen.Closed())
	require.False(t, TaskInProgress.Closed())
	require.True(t, TaskDone.Closed())
	require.True(t, TaskDropped.Closed())
}
