package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/arctop/seamless/internal/config"
)

func TestUpdateOverride(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// No row: nothing is overridden, and there is no error to fall back from.
	got, found, err := UpdateOverride(ctx, db)
	require.NoError(t, err)
	require.False(t, found)
	require.Equal(t, config.UpdateOverride{}, got)

	// A saved override round-trips both pointers, false included: a stored
	// false is a statement, not an absent field.
	saved := config.UpdateOverride{Check: new(false), Auto: new(true)}
	require.NoError(t, SetUpdateOverride(ctx, db, saved))
	got, found, err = UpdateOverride(ctx, db)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, saved, got)

	// A nil field is left out of the row and reads back nil: no override for
	// that toggle. The row itself still exists.
	require.NoError(t, SetUpdateOverride(ctx, db, config.UpdateOverride{Check: new(true)}))
	raw, _, err := GetSetting(ctx, db, SettingUpdateConfig)
	require.NoError(t, err)
	require.JSONEq(t, `{"check":true}`, raw)
	got, found, err = UpdateOverride(ctx, db)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, new(true), got.Check)
	require.Nil(t, got.Auto)

	// Clearing removes the row; clearing twice is a no-op.
	require.NoError(t, ClearUpdateOverride(ctx, db))
	require.NoError(t, ClearUpdateOverride(ctx, db))
	got, found, err = UpdateOverride(ctx, db)
	require.NoError(t, err)
	require.False(t, found)
	require.Equal(t, config.UpdateOverride{}, got)
}

func TestUpdateOverride_StoredRows(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		want      config.UpdateOverride
		wantFound bool
		wantErr   bool
	}{
		{name: "blank row reads as no row", raw: "  ", wantFound: false},
		{name: "empty object overrides nothing", raw: `{}`, wantFound: true},
		{name: "field written by a newer version is ignored", raw: `{"check":false,"channel":"beta"}`,
			want: config.UpdateOverride{Check: new(false)}, wantFound: true},
		// A corrupt row is an error, never a zero override that looks real: the
		// caller logs it and falls back to the file/env values.
		{name: "corrupt row", raw: `{not json`, wantErr: true},
		{name: "wrong shape", raw: `[true]`, wantErr: true},
		{name: "wrong field type", raw: `{"check":"yes"}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t)
			ctx := context.Background()
			require.NoError(t, SetSetting(ctx, db, SettingUpdateConfig, tt.raw))

			got, found, err := UpdateOverride(ctx, db)
			if tt.wantErr {
				require.ErrorContains(t, err, "store.UpdateOverride: decode")
				require.False(t, found)
				require.Equal(t, config.UpdateOverride{}, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantFound, found)
			require.Equal(t, tt.want, got)
		})
	}
}
