package repository

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRollupTrendAligned(t *testing.T) {
	for _, tc := range []struct {
		name, zone, start, end string
		want                   bool
	}{
		{"UTC partial hours", "UTC", "2026-04-04T14:45:00Z", "2026-04-04T16:15:00Z", true},
		{"half-hour DST inside range", "Australia/Lord_Howe", "2026-04-04T14:45:00Z", "2026-04-04T16:15:00Z", false},
		{"excluded DST endpoint", "Australia/Lord_Howe", "2026-04-04T14:45:00Z", "2026-04-04T15:00:00Z", true},
		{"included DST endpoint", "Australia/Lord_Howe", "2026-04-04T14:45:00Z", "2026-04-04T15:00:00.000000001Z", false},
		{"half-hour offset at start", "Australia/Lord_Howe", "2026-04-04T15:00:00Z", "2026-04-04T16:00:00Z", false},
		{"non-hour transition", "America/Goose_Bay", "2010-11-07T03:00:00Z", "2010-11-07T04:00:00Z", false},
		{"hour DST spring", "America/New_York", "2026-03-08T06:00:00Z", "2026-03-08T09:00:00Z", true},
		{"hour DST fall", "America/New_York", "2026-11-01T05:00:00Z", "2026-11-01T08:00:00Z", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useGroupUsageRepositoryTestTimezone(t, tc.zone)
			start, err := time.Parse(time.RFC3339Nano, tc.start)
			require.NoError(t, err)
			end, err := time.Parse(time.RFC3339Nano, tc.end)
			require.NoError(t, err)
			for _, granularity := range []string{"hour", "day"} {
				require.Equal(t, tc.want, rollupTrendAligned(start, end, granularity), granularity)
			}
		})
	}
	t.Run("extrapolated leap-year end terminates", func(t *testing.T) {
		useGroupUsageRepositoryTestTimezone(t, "America/New_York")
		start := time.Date(2028, 12, 30, 23, 0, 0, 0, time.UTC)
		// The result may be false with Go's synthetic year-end ZoneBounds.
		// Either path is safe; this call must finish instead of repeating a bound.
		_ = rollupTrendAligned(start, start.Add(2*time.Hour), "hour")
	})
}
