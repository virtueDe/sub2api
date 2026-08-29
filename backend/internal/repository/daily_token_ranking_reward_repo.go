package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type dailyTokenRankingRewardRepository struct {
	db *sql.DB
}

func NewDailyTokenRankingRewardRepository(db *sql.DB) service.DailyTokenRankingRewardRepository {
	return &dailyTokenRankingRewardRepository{db: db}
}

func (r *dailyTokenRankingRewardRepository) ListByDate(ctx context.Context, rewardDate string) ([]service.DailyTokenRankingRewardEntry, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT rank, COALESCE(user_id, 0), display_name, total_tokens, request_count,
			reward_amount, status, reason, note, settled_at
		FROM daily_token_ranking_rewards
		WHERE reward_date = $1
		ORDER BY rank ASC`, rewardDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]service.DailyTokenRankingRewardEntry, 0, 3)
	for rows.Next() {
		var entry service.DailyTokenRankingRewardEntry
		if err := rows.Scan(&entry.Rank, &entry.UserID, &entry.DisplayName, &entry.TotalTokens, &entry.RequestCount, &entry.RewardAmount, &entry.Status, &entry.Reason, &entry.Note, &entry.SettledAt); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (r *dailyTokenRankingRewardRepository) Settle(ctx context.Context, rewardDate string, operatorID int64, entries []service.DailyTokenRankingRewardEntry) ([]service.DailyTokenRankingRewardEntry, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('daily_token_ranking_reward:' || $1))`, rewardDate); err != nil {
		return nil, err
	}
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM daily_token_ranking_rewards WHERE reward_date = $1`, rewardDate).Scan(&existing); err != nil {
		return nil, err
	}
	if existing > 0 {
		return nil, fmt.Errorf("daily token ranking rewards for %s have already been settled", rewardDate)
	}

	settledAt := time.Now().UTC()
	result := make([]service.DailyTokenRankingRewardEntry, 0, len(entries))
	if len(entries) == 0 {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO daily_token_ranking_rewards
			(reward_date, rank, display_name, status, reason, note, operator_id, settled_at)
			VALUES ($1, 0, '', 'empty', '当天没有符合条件的用户', $2, $3, $4)`, rewardDate,
			fmt.Sprintf("%s 每日 Token 排行奖励，无符合条件用户", rewardDate), operatorID, settledAt); err != nil {
			return nil, err
		}
	}
	for _, entry := range entries {
		entry.Note = fmt.Sprintf("%s 每日 Token 排行奖励，第 %d 名", rewardDate, entry.Rank)
		var updatedUserID int64
		err := tx.QueryRowContext(ctx, `
			UPDATE users
			SET balance = balance + $1, updated_at = NOW()
			WHERE id = $2 AND role = 'user' AND status = 'active' AND deleted_at IS NULL
			RETURNING id`, entry.RewardAmount, entry.UserID).Scan(&updatedUserID)
		if err == sql.ErrNoRows {
			entry.Status = "skipped"
			entry.Reason = "用户当前不是正常状态的普通用户"
			entry.SettledAt = &settledAt
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO daily_token_ranking_rewards
				(reward_date, rank, user_id, display_name, total_tokens, request_count, reward_amount, status, reason, note, operator_id, settled_at)
				VALUES ($1, $2, NULLIF($3, 0), $4, $5, $6, $7, $8, $9, $10, $11, $12)`, rewardDate, entry.Rank, entry.UserID, entry.DisplayName, entry.TotalTokens, entry.RequestCount, entry.RewardAmount, entry.Status, entry.Reason, entry.Note, operatorID, settledAt); err != nil {
				return nil, err
			}
			result = append(result, entry)
			continue
		}
		if err != nil {
			return nil, err
		}
		code, err := service.GenerateRedeemCode()
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO redeem_codes (code, type, value, status, used_by, used_at, notes)
			VALUES ($1, 'balance', $2, 'used', $3, $4, $5)`, code, entry.RewardAmount, updatedUserID, settledAt, entry.Note); err != nil {
			return nil, err
		}
		entry.Status = "paid"
		entry.SettledAt = &settledAt
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO daily_token_ranking_rewards
			(reward_date, rank, user_id, display_name, total_tokens, request_count, reward_amount, status, reason, note, operator_id, settled_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`, rewardDate, entry.Rank, updatedUserID, entry.DisplayName, entry.TotalTokens, entry.RequestCount, entry.RewardAmount, entry.Status, entry.Reason, entry.Note, operatorID, settledAt); err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}
