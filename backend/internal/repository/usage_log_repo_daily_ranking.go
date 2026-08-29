package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

// GetDailyTokenRanking aggregates successful billed usage for active users.
func (r *usageLogRepository) GetDailyTokenRanking(
	ctx context.Context,
	startTime, endTime time.Time,
	limit int,
) (results []usagestats.DailyTokenRankingSource, err error) {
	return r.getDailyTokenRanking(ctx, startTime, endTime, limit, true)
}

// GetDailyTokenRankingForSettlement includes ordinary users whose status may
// have changed after the ranking day, so settlement can record a skipped award
// instead of silently dropping a previous winner.
func (r *usageLogRepository) GetDailyTokenRankingForSettlement(
	ctx context.Context,
	startTime, endTime time.Time,
	limit int,
) ([]usagestats.DailyTokenRankingSource, error) {
	return r.getDailyTokenRanking(ctx, startTime, endTime, limit, false)
}

func (r *usageLogRepository) getDailyTokenRanking(
	ctx context.Context,
	startTime, endTime time.Time,
	limit int,
	activeOnly bool,
) (results []usagestats.DailyTokenRankingSource, err error) {
	userFilter := "u.role = 'user'"
	if activeOnly {
		userFilter += " AND u.status = 'active' AND u.deleted_at IS NULL"
	}
	query := `
		SELECT
			u.id,
			COALESCE(u.email, '') AS email,
			COALESCE(u.username, '') AS username,
			COUNT(*)::bigint AS request_count,
			COALESCE(SUM(
				ul.input_tokens + ul.output_tokens +
				ul.cache_creation_tokens + ul.cache_read_tokens
			), 0)::bigint AS total_tokens
		FROM usage_logs ul
		JOIN users u ON u.id = ul.user_id
		WHERE ul.created_at >= $1
			AND ul.created_at < $2
			AND ` + usageLogSuccessFilterUL + `
			AND ` + userFilter + `
		GROUP BY u.id, u.email, u.username
		HAVING SUM(
			ul.input_tokens + ul.output_tokens +
			ul.cache_creation_tokens + ul.cache_read_tokens
		) > 0
		ORDER BY total_tokens DESC, u.id ASC
		LIMIT $3
	`

	rows, err := r.sql.QueryContext(ctx, query, startTime, endTime, limit)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results = make([]usagestats.DailyTokenRankingSource, 0, limit)
	for rows.Next() {
		var row usagestats.DailyTokenRankingSource
		if err = rows.Scan(&row.UserID, &row.Email, &row.Username, &row.RequestCount, &row.TotalTokens); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}

	return results, nil
}
