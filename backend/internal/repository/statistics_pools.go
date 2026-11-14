package repository

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/dbstats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// Only explicitly governed reporting methods receive these pools. The normal
// *sql.DB and Ent providers continue to expose the core pool to critical work.
type StatisticsPools struct {
	Core, Read, Maintenance *sql.DB
	isolated                bool
	once                    sync.Once
}

func ProvideStatisticsPools(core *sql.DB, cfg *config.Config) (*StatisticsPools, func(), error) {
	p := &StatisticsPools{Core: core, Read: core, Maintenance: core}
	if !cfg.Database.StatisticsIsolationEnabled {
		return p, p.Close, nil
	}
	if cfg.Database.MaxOpenConns < 3 {
		return nil, nil, errors.New("statistics isolation requires total max_open_conns >= 3")
	}
	p.isolated = true
	open := func(readOnly bool) (*sql.DB, error) {
		dsn := cfg.Database.DSNWithTimezone(cfg.Timezone) + " connect_timeout=2 max_parallel_workers_per_gather=0"
		if readOnly {
			dsn += " application_name=sub2api_statistics_read statement_timeout=2000 default_transaction_read_only=on"
		} else {
			dsn += " application_name=sub2api_statistics_maintenance statement_timeout=30000"
		}
		connector, err := pq.NewConnector(dsn)
		if err != nil {
			return nil, err
		}
		var db *sql.DB
		if cfg.Server.EnableServerTiming {
			db = sql.OpenDB(newServerTimingConnector(connector))
		} else {
			db = sql.OpenDB(connector)
		}
		settings := clampDBPoolSettings(cfg)
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		db.SetConnMaxLifetime(settings.ConnMaxLifetime)
		db.SetConnMaxIdleTime(settings.ConnMaxIdleTime)
		return db, nil
	}
	var err error
	p.Read, err = open(true)
	if err != nil {
		return nil, nil, err
	}
	p.Maintenance, err = open(false)
	if err != nil {
		_ = p.Read.Close()
		return nil, nil, err
	}
	return p, p.Close, nil
}
func (p *StatisticsPools) Close() {
	if p != nil && p.isolated {
		p.once.Do(func() { _ = p.Read.Close(); _ = p.Maintenance.Close() })
	}
}

func ProvideDashboardAggregationRepository(p *StatisticsPools) service.DashboardAggregationRepository {
	return NewDashboardAggregationRepository(p.Maintenance)
}
func ProvideUsageRollupRepository(p *StatisticsPools) service.UsageRollupRepository {
	return NewUsageRollupRepository(p.Maintenance)
}
func ProvideImageProfitRepository(core *sql.DB, p *StatisticsPools) service.ImageProfitRepository {
	return &governedImageProfitRepository{imageProfitRepository: &imageProfitRepository{db: core}, reporting: &imageProfitRepository{db: p.Read}, maintenance: &imageProfitRepository{db: p.Maintenance}}
}
func newGovernedUsageLogRepository(client *dbent.Client, core *sql.DB, p *StatisticsPools, cfg *config.Config) service.UsageLogRepository {
	raw := newUsageLogRepositoryWithSQL(client, core)
	reporting := newUsageLogRepositoryWithSQL(client, p.Read)
	raw.rollupReads = cfg.Database.UsageRollupReadsEnabled
	raw.rollupKeys = cfg.Database.UsageRollupReadKeyIDs
	reporting.rollupReads = raw.rollupReads
	reporting.rollupKeys = raw.rollupKeys
	raw.batchRollupDisabled = cfg.Database.UsageBatchRollupDisabled
	reporting.batchRollupDisabled = raw.batchRollupDisabled
	return &governedUsageLogRepository{usageLogRepository: raw, reporting: reporting, extendedBatchRead: p.isolated}
}

func ProvideDatabaseDiagnostics(p *StatisticsPools, cfg *config.Config) (*dbstats.Recorder, func(), error) {
	if !cfg.Database.DiagnosticsEnabled {
		return nil, func() {}, nil
	}
	dir := cfg.Database.DiagnosticsDirectory
	if dir == "" {
		base := os.Getenv("DATA_DIR")
		if base == "" {
			base = "data"
		}
		dir = filepath.Join(base, "logs", "database-pressure")
	}
	pools := map[string]func() sql.DBStats{"core": p.Core.Stats}
	var probe func(context.Context) dbstats.DatabaseProbe
	if p.isolated {
		pools["statistics_read"] = p.Read.Stats
		pools["statistics_maintenance"] = p.Maintenance.Stats
		probe = databaseWaitProbe(p.Read)
	}
	recorder, err := dbstats.NewRecorder(dir, pools, probe)
	if err != nil {
		return nil, nil, err
	}
	return recorder, recorder.Stop, nil
}

type databaseWaitQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func databaseWaitProbe(db databaseWaitQuerier) func(context.Context) dbstats.DatabaseProbe {
	return func(ctx context.Context) dbstats.DatabaseProbe {
		sample := dbstats.DatabaseProbe{At: time.Now().UTC(), Status: "ok", Visibility: "own_role_only"}
		fail := func(err error) dbstats.DatabaseProbe {
			sample.Status = "query_failed"
			if dbstats.IsTimeout(err) {
				sample.Status = "timeout"
			}
			return sample
		}
		var full bool
		if err := db.QueryRowContext(ctx, `SELECT current_setting('is_superuser')='on' OR pg_has_role(current_user,'pg_read_all_stats','usage')`).Scan(&full); err != nil {
			return fail(err)
		}
		if full {
			sample.Visibility = "all_roles"
		}
		rows, err := db.QueryContext(ctx, `WITH activity AS MATERIALIZED (
 SELECT pid,state,query,wait_event_type,wait_event,query_start,
 CASE WHEN wait_event_type='Lock' THEN pg_blocking_pids(pid) ELSE ARRAY[]::integer[] END AS blockers
 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND state IS DISTINCT FROM 'idle'
 ORDER BY (wait_event_type='Lock') DESC NULLS LAST, query_start NULLS LAST LIMIT 33
 ) SELECT pid,COALESCE(state,''),
 CASE WHEN query ILIKE '%usage_billing%' THEN 'billing_dedup' WHEN query ILIKE '%usage_settlements%' THEN 'settlement'
 WHEN query ILIKE '%usage_logs%' THEN 'usage_logs' WHEN query ILIKE '%ops_%' THEN 'ops_log'
 WHEN query ILIKE '%usage_key_%' THEN 'usage_rollup' WHEN query ILIKE 'UPDATE users%' THEN 'balance' ELSE 'other' END,
 COALESCE(wait_event_type,''),COALESCE(wait_event,''),COALESCE(extract(epoch FROM clock_timestamp()-query_start),0),
 blockers[1:16],cardinality(blockers)
 FROM activity ORDER BY (wait_event_type='Lock') DESC NULLS LAST, query_start NULLS LAST`)
		if err != nil {
			return fail(err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			if len(sample.Waits) == 32 {
				sample.Truncated = true
				break
			}
			var w dbstats.DatabaseWait
			var blockers pq.Int64Array
			if err = rows.Scan(&w.PID, &w.State, &w.Category, &w.WaitType, &w.WaitEvent, &w.AgeSeconds, &blockers, &w.BlockerCount); err != nil {
				return fail(err)
			}
			w.Blockers = []int64(blockers)
			w.BlockersTruncated = w.BlockerCount > len(w.Blockers)
			sample.Waits = append(sample.Waits, w)
		}
		if err = rows.Err(); err != nil {
			return fail(err)
		}
		return sample
	}
}
