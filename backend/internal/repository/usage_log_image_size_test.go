package repository

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageLogImageSizeMetadataDoesNotBlockBilling(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		valid bool
	}{
		{"dimensions", "1024x1536", true},
		{"custom", "provider-custom-size", true},
		{"unicode_at_limit", strings.Repeat("中", 32), true},
		{"unicode_over_limit", strings.Repeat("中", 33), false},
		{"legacy_description", strings.Repeat("description ", 20), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := &service.UsageLog{RequestID: "settlement:size-test", ImageCount: 3, ActualCost: .0210006, ImageInputSize: &tc.value, ImageOutputSize: &tc.value}
			prepared := prepareUsageLogInsert(log)
			want := sql.NullString{}
			if tc.valid {
				want = sql.NullString{String: tc.value, Valid: true}
			}
			require.Equal(t, want, prepared.args[39])
			require.Equal(t, want, prepared.args[40])
			require.Equal(t, tc.value, *log.ImageInputSize, "do not mutate recorded facts")
			require.Equal(t, tc.value, *log.ImageOutputSize)
			require.Equal(t, .0210006, prepared.args[26], "metadata must not change the charge")
			require.Equal(t, 3, prepared.args[37])
		})
	}
}
