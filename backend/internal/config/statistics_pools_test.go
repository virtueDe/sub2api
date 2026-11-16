package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadStatisticsReadPoolCapacity(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("DATABASE_STATISTICS_ISOLATION_ENABLED", "true")
	t.Setenv("DATABASE_MAX_OPEN_CONNS", "50")
	t.Setenv("DATABASE_MAX_IDLE_CONNS", "10")
	t.Setenv("DATABASE_STATISTICS_READ_MAX_OPEN_CONNS", "")
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, 1, cfg.Database.StatisticsReadMaxOpenConns)
	t.Setenv("DATABASE_STATISTICS_READ_MAX_OPEN_CONNS", "2")
	cfg, err = Load()
	require.NoError(t, err)
	require.Equal(t, 2, cfg.Database.StatisticsReadMaxOpenConns)
}

func TestValidateStatisticsPoolBudget(t *testing.T) {
	for _, tt := range []struct {
		name     string
		total    int
		read     int
		isolated bool
		valid    bool
	}{
		{"legacy minimum", 3, 1, true, true},
		{"two readers", 50, 2, true, true},
		{"one core remains", 4, 2, true, true},
		{"no core remains", 3, 2, true, false},
		{"unlimited total rejected", 0, 1, true, false},
		{"unlimited read rejected", 50, 0, true, false},
		{"negative read rejected", 50, -1, true, false},
		{"disabled isolation ignores read size", 50, 0, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := DatabaseConfig{MaxOpenConns: tt.total, StatisticsReadMaxOpenConns: tt.read, StatisticsIsolationEnabled: tt.isolated}
			if tt.valid {
				require.NoError(t, d.ValidateStatisticsPools())
			} else {
				require.Error(t, d.ValidateStatisticsPools())
			}
		})
	}
}
