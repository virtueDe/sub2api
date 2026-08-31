package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"log"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

const (
    SettingKeyCheckInEnabled             = "check_in_enabled"
    SettingKeyCheckInRewardMin           = "check_in_reward_min"
    SettingKeyCheckInRewardMax           = "check_in_reward_max"
    SettingKeyCheckInCondition           = "check_in_condition"
    SettingKeyCheckInRequestThreshold    = "check_in_request_threshold"
    SettingKeyCheckInConsumptionThreshold = "check_in_consumption_threshold"
)

var (
    ErrCheckInDisabled = infraerrors.BadRequest("CHECK_IN_DISABLED", "check-in is disabled")
    ErrCheckInAlreadyDone = infraerrors.Conflict("CHECK_IN_ALREADY_DONE", "already checked in today")
    ErrCheckInRequestThresholdNotMet = infraerrors.BadRequest("CHECK_IN_REQUEST_THRESHOLD_NOT_MET", "daily request count does not meet the check-in requirement")
    ErrCheckInConsumptionThresholdNotMet = infraerrors.BadRequest("CHECK_IN_CONSUMPTION_THRESHOLD_NOT_MET", "daily consumption does not meet the check-in requirement")
)

type CheckInConfig struct {
    Enabled bool `json:"enabled"`
    RewardMin float64 `json:"reward_min"`
    RewardMax float64 `json:"reward_max"`
    Condition string `json:"condition"` // request | consumption
    RequestThreshold float64 `json:"request_threshold,omitempty"`
    ConsumptionThreshold float64 `json:"consumption_threshold,omitempty"`
}

type CheckInRecord struct {
    ID int64 `json:"id"`
    UserID int64 `json:"user_id"`
    Email string `json:"email,omitempty"`
    Username string `json:"username,omitempty"`
    CheckInDate string `json:"check_in_date"`
    Reward float64 `json:"reward"`
    RequestCount int64 `json:"request_count"`
    DailySpend float64 `json:"daily_spend"`
    CreatedAt time.Time `json:"created_at"`
}

type CheckInStatus struct {
	Enabled bool `json:"enabled"`
    CheckedInToday bool `json:"checked_in_today"`
    TodayReward float64 `json:"today_reward"`
    TotalReward float64 `json:"total_reward"`
    StreakDays int `json:"streak_days"`
    Account CheckInAccount `json:"account"`
    Date string `json:"date"`
    Config CheckInConfig `json:"config"`
}

type CheckInAccount struct {
    ID int64 `json:"id"`
    Username string `json:"username"`
    Email string `json:"email"`
    Balance float64 `json:"balance"`
}

type CheckInService struct {
    db *sql.DB
    settingRepo SettingRepository
    billingCacheService *BillingCacheService
}

func NewCheckInService(db *sql.DB, settingRepo SettingRepository, billingCacheService *BillingCacheService) *CheckInService {
    return &CheckInService{db: db, settingRepo: settingRepo, billingCacheService: billingCacheService}
}

func (s *CheckInService) GetConfig(ctx context.Context) (CheckInConfig, error) {
    values, err := s.settingRepo.GetMultiple(ctx, []string{
        SettingKeyCheckInEnabled, SettingKeyCheckInRewardMin, SettingKeyCheckInRewardMax,
        SettingKeyCheckInCondition, SettingKeyCheckInRequestThreshold, SettingKeyCheckInConsumptionThreshold,
    })
    if err != nil { return CheckInConfig{}, err }
    cfg := CheckInConfig{Enabled: values[SettingKeyCheckInEnabled] == "true", RewardMin: 1, RewardMax: 1, Condition: ""}
    cfg.RewardMin = parseCheckInAmount(values[SettingKeyCheckInRewardMin], 1)
    cfg.RewardMax = parseCheckInAmount(values[SettingKeyCheckInRewardMax], 1000)
    if cfg.RewardMin < 1 || cfg.RewardMin > 1000 { cfg.RewardMin = 1 }
    if cfg.RewardMax < 1 || cfg.RewardMax > 1000 { cfg.RewardMax = 1000 }
    cfg.RewardMin = math.Round(cfg.RewardMin*10) / 10
    cfg.RewardMax = math.Round(cfg.RewardMax*10) / 10
    if cfg.RewardMin > cfg.RewardMax { cfg.RewardMin, cfg.RewardMax = 1, 1000 }
    cfg.Condition = strings.TrimSpace(values[SettingKeyCheckInCondition])
    if cfg.Condition != "request" && cfg.Condition != "consumption" { cfg.Condition = "" }
    cfg.RequestThreshold = parseCheckInAmount(values[SettingKeyCheckInRequestThreshold], 0)
    cfg.ConsumptionThreshold = parseCheckInAmount(values[SettingKeyCheckInConsumptionThreshold], 0)
    return cfg, nil
}

func (s *CheckInService) UpdateConfig(ctx context.Context, cfg CheckInConfig) (CheckInConfig, error) {
    cfg.RewardMin = math.Round(cfg.RewardMin*10) / 10
    cfg.RewardMax = math.Round(cfg.RewardMax*10) / 10
    if cfg.RewardMin < 1 || cfg.RewardMin > 1000 || cfg.RewardMax < 1 || cfg.RewardMax > 1000 || cfg.RewardMin > cfg.RewardMax {
        return CheckInConfig{}, infraerrors.BadRequest("CHECK_IN_REWARD_RANGE_INVALID", "reward range must be between 1 and 1000, with min <= max")
    }
    if cfg.Condition != "" && cfg.Condition != "request" && cfg.Condition != "consumption" {
        return CheckInConfig{}, infraerrors.BadRequest("CHECK_IN_CONDITION_INVALID", "invalid check-in condition")
    }
    if cfg.Condition == "request" && (cfg.RequestThreshold < 1 || math.Trunc(cfg.RequestThreshold) != cfg.RequestThreshold) { return CheckInConfig{}, infraerrors.BadRequest("CHECK_IN_REQUEST_THRESHOLD_INVALID", "request threshold must be a positive integer") }
    if cfg.Condition == "consumption" && cfg.ConsumptionThreshold <= 0 { return CheckInConfig{}, infraerrors.BadRequest("CHECK_IN_CONSUMPTION_THRESHOLD_INVALID", "consumption threshold must be greater than 0") }
    err := s.settingRepo.SetMultiple(ctx, map[string]string{
        SettingKeyCheckInEnabled: strconv.FormatBool(cfg.Enabled),
        SettingKeyCheckInRewardMin: formatCheckInAmount(cfg.RewardMin),
        SettingKeyCheckInRewardMax: formatCheckInAmount(cfg.RewardMax),
        SettingKeyCheckInCondition: cfg.Condition,
        SettingKeyCheckInRequestThreshold: formatCheckInAmount(cfg.RequestThreshold),
        SettingKeyCheckInConsumptionThreshold: formatCheckInAmount(cfg.ConsumptionThreshold),
    })
    if err != nil { return CheckInConfig{}, err }
    return cfg, nil
}

func (s *CheckInService) GetStatus(ctx context.Context, userID int64) (CheckInStatus, error) {
	cfg, err := s.GetConfig(ctx); if err != nil { return CheckInStatus{}, err }
    now := timezone.Now()
    today := now.Format("2006-01-02")
    monthStart := now.Format("2006-01-02")[:8] + "01"
    var status CheckInStatus
    var username, email string
    if err := s.db.QueryRowContext(ctx, `SELECT id, username, email, balance FROM users WHERE id = $1 AND deleted_at IS NULL`, userID).Scan(&status.Account.ID, &username, &email, &status.Account.Balance); err != nil { return status, err }
	status.Enabled = cfg.Enabled
	status.Config = cfg
	status.Account.Username, status.Account.Email, status.Date = username, email, today
    var todayReward, totalReward float64
    var checked bool
    if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM check_in_records WHERE user_id=$1 AND check_in_date=$2), COALESCE((SELECT reward FROM check_in_records WHERE user_id=$1 AND check_in_date=$2),0), COALESCE((SELECT SUM(reward) FROM check_in_records WHERE user_id=$1 AND check_in_date >= $3::date AND check_in_date < ($3::date + INTERVAL '1 month')),0)`, userID, today, monthStart).Scan(&checked, &todayReward, &totalReward); err != nil { return status, err }
    status.CheckedInToday, status.TodayReward, status.TotalReward = checked, todayReward, totalReward
    rows, err := s.db.QueryContext(ctx, `SELECT check_in_date::text FROM check_in_records WHERE user_id=$1 AND check_in_date <= $2::date ORDER BY check_in_date DESC`, userID, today)
    if err != nil {
        return status, err
    }
    defer rows.Close()
    expected := timezone.StartOfDay(now)
    for rows.Next() {
        var dateText string
        if scanErr := rows.Scan(&dateText); scanErr != nil {
            return status, scanErr
        }
        checkedDate, parseErr := time.ParseInLocation("2006-01-02", dateText, timezone.Location())
        if parseErr != nil || !checkedDate.Equal(expected) {
            break
        }
        status.StreakDays++
        expected = expected.AddDate(0, 0, -1)
    }
    if rowsErr := rows.Err(); rowsErr != nil {
        return status, rowsErr
    }
    return status, nil
}

func (s *CheckInService) CheckIn(ctx context.Context, userID int64) (CheckInRecord, CheckInStatus, error) {
    cfg, err := s.GetConfig(ctx); if err != nil { return CheckInRecord{}, CheckInStatus{}, err }
    if !cfg.Enabled { return CheckInRecord{}, CheckInStatus{}, ErrCheckInDisabled }
    tx, err := s.db.BeginTx(ctx, nil); if err != nil { return CheckInRecord{}, CheckInStatus{}, err }
    defer tx.Rollback()
    var role, state string
    var balance float64
    if err := tx.QueryRowContext(ctx, `SELECT role,status,balance FROM users WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, userID).Scan(&role, &state, &balance); err != nil { return CheckInRecord{}, CheckInStatus{}, err }
    if role != RoleUser || state != StatusActive { return CheckInRecord{}, CheckInStatus{}, ErrInsufficientPerms }
    now := timezone.Now()
    today := now.Format("2006-01-02")
    dayStart := timezone.StartOfDay(now).UTC()
    dayEnd := dayStart.Add(24 * time.Hour)
    var exists bool
    if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM check_in_records WHERE user_id=$1 AND check_in_date=$2)`, userID, today).Scan(&exists); err != nil { return CheckInRecord{}, CheckInStatus{}, err }
    if exists { return CheckInRecord{}, CheckInStatus{}, ErrCheckInAlreadyDone }
    var requests int64; var spend float64
    if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(actual_cost),0) FROM usage_logs WHERE user_id=$1 AND actual_cost > 0 AND created_at >= $2 AND created_at < $3`, userID, dayStart, dayEnd).Scan(&requests, &spend); err != nil { return CheckInRecord{}, CheckInStatus{}, err }
    if cfg.Condition == "request" && requests < int64(cfg.RequestThreshold) {
        return CheckInRecord{}, CheckInStatus{}, ErrCheckInRequestThresholdNotMet.WithMetadata(map[string]string{
            "current": strconv.FormatInt(requests, 10),
            "required": strconv.FormatInt(int64(cfg.RequestThreshold), 10),
        })
    }
    if cfg.Condition == "consumption" && spend < cfg.ConsumptionThreshold {
        return CheckInRecord{}, CheckInStatus{}, ErrCheckInConsumptionThresholdNotMet.WithMetadata(map[string]string{
            "current": strconv.FormatFloat(spend, 'f', -1, 64),
            "required": strconv.FormatFloat(cfg.ConsumptionThreshold, 'f', -1, 64),
        })
    }
    reward, err := randomCheckInReward(cfg.RewardMin, cfg.RewardMax); if err != nil { return CheckInRecord{}, CheckInStatus{}, err }
    if _, err = tx.ExecContext(ctx, `UPDATE users SET balance=balance+$1, total_recharged=total_recharged+$1, updated_at=NOW() WHERE id=$2`, reward, userID); err != nil { return CheckInRecord{}, CheckInStatus{}, err }
    var record CheckInRecord
    err = tx.QueryRowContext(ctx, `INSERT INTO check_in_records(user_id,check_in_date,reward,request_count,daily_spend) VALUES($1,$2,$3,$4,$5) RETURNING id,check_in_date::text,reward,request_count,daily_spend,created_at`, userID, today, reward, requests, spend).Scan(&record.ID, &record.CheckInDate, &record.Reward, &record.RequestCount, &record.DailySpend, &record.CreatedAt)
    if err != nil { return CheckInRecord{}, CheckInStatus{}, err }
    if err = tx.Commit(); err != nil { return CheckInRecord{}, CheckInStatus{}, err }
    if s.billingCacheService != nil {
        if cacheErr := s.billingCacheService.InvalidateUserBalance(ctx, userID); cacheErr != nil {
            log.Printf("[check-in] invalidate balance cache failed for user %d: %v", userID, cacheErr)
        }
    }
    status, err := s.GetStatus(ctx, userID); return record, status, err
}

func (s *CheckInService) ListRecords(ctx context.Context, userID int64, month string, page, pageSize int) ([]CheckInRecord, int64, error) {
    if page < 1 { page = 1 }; if pageSize < 1 || pageSize > 100 { pageSize = 20 }
    if _, err := time.Parse("2006-01", month); err != nil { month = timezone.Now().Format("2006-01") }
    where := `WHERE r.check_in_date >= ($1 || '-01')::date AND r.check_in_date < (($1 || '-01')::date + INTERVAL '1 month')`; args := []any{month}; idx := 2
    if userID > 0 { where += fmt.Sprintf(" AND r.user_id=$%d", idx); args = append(args, userID); idx++ }
    var total int64; if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM check_in_records r "+where, args...).Scan(&total); err != nil { return nil, 0, err }
    args = append(args, pageSize, (page-1)*pageSize)
    rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT r.id,r.user_id,u.email,u.username,r.check_in_date::text,r.reward,r.request_count,r.daily_spend,r.created_at FROM check_in_records r JOIN users u ON u.id=r.user_id %s ORDER BY r.check_in_date DESC,r.id DESC LIMIT $%d OFFSET $%d`, where, idx, idx+1), args...); if err != nil { return nil, 0, err }; defer rows.Close()
    records := []CheckInRecord{}; for rows.Next() { var r CheckInRecord; if err := rows.Scan(&r.ID,&r.UserID,&r.Email,&r.Username,&r.CheckInDate,&r.Reward,&r.RequestCount,&r.DailySpend,&r.CreatedAt); err != nil { return nil,0,err }; records=append(records,r) }; return records,total,rows.Err()
}

func randomCheckInReward(minimum, maximum float64) (float64, error) {
    low, high := int64(minimum*10+0.5), int64(maximum*10+0.5); n, err := rand.Int(rand.Reader, big.NewInt(high-low+1)); if err != nil { return 0, err }; return float64(low+n.Int64())/10, nil
}
func parseCheckInAmount(raw string, fallback float64) float64 { v, err := strconv.ParseFloat(strings.TrimSpace(raw),64); if err != nil { return fallback }; return v }
func formatCheckInAmount(v float64) string { return strconv.FormatFloat(v,'f',1,64) }
