package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/dbstats"
	"github.com/stretchr/testify/require"
)

func TestStatisticsPoolCapacityAndSharedAdmission(t *testing.T) {
	previousBudget := dbstats.Default
	t.Cleanup(func() { dbstats.Default = previousBudget })
	cfg := &config.Config{Database: config.DatabaseConfig{
		Host: "localhost", Port: 5432, User: "postgres", DBName: "postgres", SSLMode: "disable",
		MaxOpenConns: 50, MaxIdleConns: 50,
		StatisticsIsolationEnabled: true, StatisticsReadMaxOpenConns: 2,
	}}
	core, err := sql.Open("postgres", cfg.Database.DSN())
	require.NoError(t, err)
	t.Cleanup(func() { _ = core.Close() })
	applyDBPoolSettings(core, cfg)
	pools, closePools, err := ProvideStatisticsPools(core, cfg)
	require.NoError(t, err)
	t.Cleanup(closePools)
	require.Equal(t, 47, core.Stats().MaxOpenConnections)
	require.Equal(t, 2, pools.Read.Stats().MaxOpenConnections)
	require.Equal(t, 1, pools.Maintenance.Stats().MaxOpenConnections)
	require.Equal(t, 50, core.Stats().MaxOpenConnections+pools.Read.Stats().MaxOpenConnections+pools.Maintenance.Stats().MaxOpenConnections)
	// A maintenance task and a reader can both run; a third task is rejected
	// before its loader can request a database connection.
	err = dbstats.RunMaintenance(context.Background(), nil, func(ctx context.Context) error {
		_, innerErr := dbstats.Run(ctx, nil, time.Second, func(ctx context.Context) (int, error) {
			_, denied := dbstats.Run(ctx, nil, time.Second, func(context.Context) (int, error) {
				t.Fatal("third statistics task entered the loader")
				return 0, nil
			})
			require.ErrorIs(t, denied, dbstats.ErrBusy)
			return 1, nil
		})
		return innerErr
	})
	require.NoError(t, err)
}

func TestStatisticsPoolRejectsBudgetWithoutCore(t *testing.T) {
	cfg := &config.Config{Database: config.DatabaseConfig{
		MaxOpenConns: 3, StatisticsIsolationEnabled: true, StatisticsReadMaxOpenConns: 2,
	}}
	_, _, err := ProvideStatisticsPools(nil, cfg)
	require.ErrorContains(t, err, "database.max_open_conns")
	_, _, err = InitEnt(cfg)
	require.ErrorContains(t, err, "database.max_open_conns")
}
