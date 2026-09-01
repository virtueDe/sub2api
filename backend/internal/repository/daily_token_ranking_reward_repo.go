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
	defer func() { _ = rows.Close() }()
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

func (r *dailyTokenRankingRewardRepository) SettleRank(ctx context.Context, rewardDate string, operatorID int64, entries []service.DailyTokenRankingRewardEntry, rank int) (service.DailyTokenRankingRewardEntry, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return service.DailyTokenRankingRewardEntry{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('daily_token_ranking_reward:' || $1))`, rewardDate); err != nil {
		return service.DailyTokenRankingRewardEntry{}, err
	}
	for _, candidate := range entries {
		if candidate.Rank < 1 || candidate.Rank > 3 {
			continue
		}
		candidate.Note = fmt.Sprintf("%s 每日 Token 排行奖励，第 %d 名", rewardDate, candidate.Rank)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO daily_token_ranking_rewards
			(reward_date, rank, user_id, display_name, total_tokens, request_count, reward_amount, status, reason, note)
			VALUES ($1, $2, NULLIF($3, 0), $4, $5, $6, $7, 'pending', '', $8)
			ON CONFLICT (reward_date, rank) DO NOTHING`, rewardDate, candidate.Rank, candidate.UserID,
			candidate.DisplayName, candidate.TotalTokens, candidate.RequestCount, candidate.RewardAmount, candidate.Note); err != nil {
			return service.DailyTokenRankingRewardEntry{}, err
		}
	}

	var entry service.DailyTokenRankingRewardEntry
	err = tx.QueryRowContext(ctx, `
		SELECT rank, COALESCE(user_id, 0), display_name, total_tokens, request_count,
			reward_amount, status, reason, note, settled_at
		FROM daily_token_ranking_rewards
		WHERE reward_date = $1 AND rank = $2`, rewardDate, rank).Scan(
		&entry.Rank, &entry.UserID, &entry.DisplayName, &entry.TotalTokens,
		&entry.RequestCount, &entry.RewardAmount, &entry.Status, &entry.Reason,
		&entry.Note, &entry.SettledAt,
	)
	if err != nil {
		return service.DailyTokenRankingRewardEntry{}, err
	}
	if entry.Status != "pending" {
		if err := tx.Commit(); err != nil {
			return service.DailyTokenRankingRewardEntry{}, err
		}
		return entry, nil
	}

	settledAt := time.Now().UTC()
	var updatedUserID int64
	err = tx.QueryRowContext(ctx, `
		UPDATE users
		SET balance = balance + $1, updated_at = NOW()
		WHERE id = $2 AND role = 'user' AND status = 'active' AND deleted_at IS NULL
		RETURNING id`, entry.RewardAmount, entry.UserID).Scan(&updatedUserID)
	if err == sql.ErrNoRows {
		entry.Status = "skipped"
		entry.Reason = "用户当前不是正常状态的普通用户"
		entry.SettledAt = &settledAt
	} else {
		if err != nil {
			return service.DailyTokenRankingRewardEntry{}, err
		}
		code, err := service.GenerateRedeemCode()
		if err != nil {
			return service.DailyTokenRankingRewardEntry{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO redeem_codes (code, type, value, status, used_by, used_at, notes)
			VALUES ($1, 'balance', $2, 'used', $3, $4, $5)`, code, entry.RewardAmount, updatedUserID, settledAt, entry.Note); err != nil {
			return service.DailyTokenRankingRewardEntry{}, err
		}
		entry.Status = "paid"
		entry.SettledAt = &settledAt
		entry.UserID = updatedUserID
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE daily_token_ranking_rewards
		SET user_id = NULLIF($3, 0), status = $4, reason = $5, operator_id = $6, settled_at = $7
		WHERE reward_date = $1 AND rank = $2`, rewardDate, entry.Rank, entry.UserID,
		entry.Status, entry.Reason, operatorID, settledAt); err != nil {
		return service.DailyTokenRankingRewardEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return service.DailyTokenRankingRewardEntry{}, err
	}
	return entry, nil
}
