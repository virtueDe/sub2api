//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/dbstats"
	"github.com/stretchr/testify/require"
)

func isolatedPoolsFixture(t *testing.T) *StatisticsPools {
	t.Helper()
	u, err := url.Parse(integrationDSN)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	password, _ := u.User.Password()
	cfg := &config.Config{Timezone: "UTC", Database: config.DatabaseConfig{Host: u.Hostname(), Port: port, User: u.User.Username(), Password: password, DBName: u.Path[1:], SSLMode: "disable", MaxOpenConns: 50, MaxIdleConns: 50, StatisticsIsolationEnabled: true}}
	core, err := sql.Open("postgres", integrationDSN)
	require.NoError(t, err)
	t.Cleanup(func() { _ = core.Close() })
	applyDBPoolSettings(core, cfg)
	pools, closePools, err := ProvideStatisticsPools(core, cfg)
	require.NoError(t, err)
	t.Cleanup(closePools)
	require.Equal(t, 50, core.Stats().MaxOpenConnections+pools.Read.Stats().MaxOpenConnections+pools.Maintenance.Stats().MaxOpenConnections)
	return pools
}
func TestStatisticsPoolsSessionLimitsAndCoreIsolation(t *testing.T) {
	p := isolatedPoolsFixture(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		var readOnly, timeout, parallel string
		require.NoError(t, p.Read.QueryRowContext(ctx, "SELECT current_setting('default_transaction_read_only'),current_setting('statement_timeout'),current_setting('max_parallel_workers_per_gather')").Scan(&readOnly, &timeout, &parallel))
		require.Equal(t, []string{"on", "2s", "0"}, []string{readOnly, timeout, parallel})
		p.Read.SetConnMaxLifetime(time.Nanosecond)
	}
	_, err := p.Read.ExecContext(ctx, "UPDATE users SET balance=balance WHERE false")
	require.ErrorContains(t, err, "read-only")
	_, err = p.Maintenance.ExecContext(ctx, "UPDATE users SET balance=balance WHERE false")
	require.NoError(t, err)
	held, err := p.Read.Conn(ctx)
	require.NoError(t, err)
	short, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	require.NoError(t, p.Core.PingContext(short))
	require.NoError(t, held.Close())
	_, err = p.Read.ExecContext(ctx, "SELECT pg_sleep(3)")
	require.True(t, dbstats.IsTimeout(err), "server must cancel the statistics query: %v", err)
	p.Close()
	require.NoError(t, p.Core.PingContext(ctx))
}
func TestDatabaseWaitProbeIdentifiesLockWithoutSQLText(t *testing.T) {
	p := isolatedPoolsFixture(t)
	ctx := context.Background()
	holder, err := p.Core.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = holder.Rollback() }()
	_, err = holder.ExecContext(ctx, "SELECT pg_advisory_xact_lock(874329119)")
	require.NoError(t, err)
	done := make(chan error, 1)
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	go func() {
		_, err := p.Core.ExecContext(waitCtx, "SELECT pg_advisory_xact_lock(874329119) /* sensitive_query_text */")
		done <- err
	}()
	var sample dbstats.DatabaseProbe
	require.Eventually(t, func() bool {
		sample = databaseWaitProbe(p.Read)(waitCtx)
		for _, wait := range sample.Waits {
			if wait.WaitType == "Lock" && len(wait.Blockers) > 0 {
				return true
			}
		}
		return false
	}, 3*time.Second, 50*time.Millisecond)
	data, err := json.Marshal(sample)
	require.NoError(t, err)
	require.NotContains(t, string(data), "sensitive_query_text")
	require.NoError(t, holder.Rollback())
	require.NoError(t, <-done)
}

func TestDatabaseWaitProbeBoundsSessionsAndBlockers(t *testing.T) {
	p := isolatedPoolsFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var holders []*sql.Tx
	defer func() {
		for _, tx := range holders {
			_ = tx.Rollback()
		}
	}()
	for i := 0; i < 33; i++ {
		tx, err := p.Core.BeginTx(ctx, nil)
		require.NoError(t, err)
		holders = append(holders, tx)
		_, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock_shared(874329120)")
		require.NoError(t, err)
	}
	done := make(chan error, 1)
	go func() { _, err := p.Core.ExecContext(ctx, "SELECT pg_advisory_xact_lock(874329120)"); done <- err }()
	var sample dbstats.DatabaseProbe
	require.Eventually(t, func() bool {
		sample = databaseWaitProbe(p.Read)(ctx)
		return len(sample.Waits) > 0 && sample.Waits[0].BlockerCount == 33
	}, 3*time.Second, 50*time.Millisecond)
	require.Equal(t, "ok", sample.Status)
	require.True(t, sample.Truncated)
	require.Len(t, sample.Waits, 32)
	require.True(t, sample.Waits[0].BlockersTruncated)
	require.Len(t, sample.Waits[0].Blockers, 16)
	for _, tx := range holders {
		require.NoError(t, tx.Rollback())
	}
	require.NoError(t, <-done)
}

func TestDatabaseWaitProbeNonInheritedRoleIsRestricted(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `CREATE ROLE sub2api_probe_noinherit NOINHERIT; GRANT pg_read_all_stats TO sub2api_probe_noinherit; SET LOCAL ROLE sub2api_probe_noinherit`)
	require.NoError(t, err)
	var member, usable bool
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT pg_has_role(current_user,'pg_read_all_stats','member'),pg_has_role(current_user,'pg_read_all_stats','usage')`).Scan(&member, &usable))
	require.True(t, member)
	require.False(t, usable)
	sample := databaseWaitProbe(tx)(ctx)
	require.Equal(t, "ok", sample.Status)
	require.Equal(t, "own_role_only", sample.Visibility)
}
