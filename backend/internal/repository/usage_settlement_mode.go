package repository

import (
	"context"
	"database/sql"
	"errors"
)

// This marker survives disabling intake and completion of the final settlement.
// Automatically returning to Redis would reuse snapshots from before DB-only
// charges (including ordinary requests that have no settlement row).
const usageSettlementQuotaModeKey = "internal_usage_settlement_db_quota_mode"

type settlementModeDB interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func initializeUsageSettlementQuotaMode(ctx context.Context, db settlementModeDB, intake, flusher bool) (bool, error) {
	var persisted bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM settings WHERE key=$1)
 OR EXISTS(SELECT 1 FROM usage_settlements)`, usageSettlementQuotaModeKey).Scan(&persisted); err != nil {
		return false, err
	}
	if !intake && !persisted {
		return false, nil
	}
	if flusher {
		return false, errors.New("DB-authoritative platform quota requires user_platform_quota_flusher_enabled=false, including after disabling settlement intake")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES($1,'true',now()) ON CONFLICT(key) DO NOTHING`, usageSettlementQuotaModeKey); err != nil {
		return false, err
	}
	return true, nil
}
