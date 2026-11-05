package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"entgo.io/ent/dialect"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/apikey"
	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/ent/setting"
	"github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/ent/userallowedgroup"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const paidRedeemDiscountSettingKey = "paid_redeem_discount"

// PaidRedeemDiscountConfig controls grants on future redemptions. Saving it does
// not change any existing user rate; historical grants require a separate preview.
type PaidRedeemDiscountConfig struct {
	Enabled        bool    `json:"enabled"`
	GroupID        int64   `json:"group_id"`
	RateMultiplier float64 `json:"rate_multiplier"`
	SourceGroupIDs []int64 `json:"source_group_ids"`
}

type PaidRedeemDiscountUser struct {
	UserID        int64    `json:"user_id"`
	Email         string   `json:"email"`
	Username      string   `json:"username"`
	CurrentRate   *float64 `json:"current_rate"`
	EffectiveRate float64  `json:"effective_rate"`
	NewRate       float64  `json:"new_rate"`
	WillChange    bool     `json:"will_change"`
}

type PaidRedeemDiscountPreview struct {
	Config       PaidRedeemDiscountConfig `json:"config"`
	GroupName    string                   `json:"group_name"`
	DefaultRate  float64                  `json:"default_rate"`
	Users        []PaidRedeemDiscountUser `json:"users"`
	UserCount    int                      `json:"user_count"`
	ChangeCount  int                      `json:"change_count"`
	PreviewToken string                   `json:"preview_token"`
}

type PaidRedeemDiscountResult struct {
	UserCount    int `json:"user_count"`
	UpdatedCount int `json:"updated_count"`
}

type PaidRedeemDiscountService struct {
	client      *dbent.Client
	invalidator APIKeyAuthCacheInvalidator
}

func NewPaidRedeemDiscountService(client *dbent.Client, invalidator APIKeyAuthCacheInvalidator) *PaidRedeemDiscountService {
	return &PaidRedeemDiscountService{client: client, invalidator: invalidator}
}

// This intentionally differs from the legacy amount-based exclusive-group sync.
// Negative adjustment codes and unmarked gifts do not grant paid discounts.
func isPaidDiscountRedeemCode(code *RedeemCode) bool {
	return code != nil && code.Type == RedeemTypeBalance &&
		code.EntitlementProfile == PaidEntitlementProfileDefault &&
		code.Value > 0 && !math.IsNaN(code.Value) && !math.IsInf(code.Value, 0)
}

func loadPaidRedeemDiscountConfig(ctx context.Context, client *dbent.Client) (*PaidRedeemDiscountConfig, error) {
	stored, err := client.Setting.Query().Where(setting.KeyEQ(paidRedeemDiscountSettingKey)).Only(ctx)
	if dbent.IsNotFound(err) {
		return &PaidRedeemDiscountConfig{RateMultiplier: 0.01, SourceGroupIDs: []int64{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load paid redeem discount: %w", err)
	}
	var cfg PaidRedeemDiscountConfig
	if err := json.Unmarshal([]byte(stored.Value), &cfg); err != nil {
		return nil, fmt.Errorf("decode paid redeem discount: %w", err)
	}
	if cfg.SourceGroupIDs == nil {
		cfg.SourceGroupIDs = []int64{}
	}
	return &cfg, nil
}

func (s *PaidRedeemDiscountService) GetConfig(ctx context.Context) (*PaidRedeemDiscountConfig, error) {
	return loadPaidRedeemDiscountConfig(ctx, s.client)
}

func validatePaidDiscountRate(rate float64) error {
	if math.IsNaN(rate) || math.IsInf(rate, 0) || rate <= 0 || rate > 1 || math.Abs(rate*10000-math.Round(rate*10000)) > 1e-8 {
		return infraerrors.BadRequest("PAID_DISCOUNT_RATE_INVALID", "discount multiplier must be between 0.0001 and 1 with at most four decimal places")
	}
	return nil
}

func paidDiscountTarget(ctx context.Context, client *dbent.Client, cfg *PaidRedeemDiscountConfig) (*dbent.Group, error) {
	if err := validatePaidDiscountRate(cfg.RateMultiplier); err != nil {
		return nil, err
	}
	target, err := client.Group.Get(ctx, cfg.GroupID)
	if dbent.IsNotFound(err) || cfg.GroupID <= 0 {
		return nil, infraerrors.BadRequest("PAID_DISCOUNT_GROUP_INVALID", "discount target group does not exist")
	}
	if err != nil {
		return nil, fmt.Errorf("load discount target: %w", err)
	}
	if target.Status != StatusActive || target.SubscriptionType == SubscriptionTypeSubscription || !target.AllowImageGeneration || target.ImageRateIndependent {
		return nil, infraerrors.BadRequest("PAID_DISCOUNT_GROUP_INVALID", "discount target must be an active image group using the shared group multiplier, without a subscription")
	}
	if math.IsNaN(target.RateMultiplier) || math.IsInf(target.RateMultiplier, 0) || target.RateMultiplier < 0 {
		return nil, infraerrors.BadRequest("PAID_DISCOUNT_GROUP_INVALID", "target group multiplier is invalid")
	}
	return target, nil
}

func (s *PaidRedeemDiscountService) UpdateConfig(ctx context.Context, cfg PaidRedeemDiscountConfig) (*PaidRedeemDiscountConfig, error) {
	if err := validatePaidDiscountRate(cfg.RateMultiplier); err != nil {
		return nil, err
	}
	if cfg.Enabled || cfg.GroupID != 0 {
		if _, err := paidDiscountTarget(ctx, s.client, &cfg); err != nil {
			return nil, err
		}
	}
	ids := make([]int64, 0, len(cfg.SourceGroupIDs))
	seen := make(map[int64]bool)
	for _, id := range cfg.SourceGroupIDs {
		if id <= 0 {
			return nil, infraerrors.BadRequest("PAID_DISCOUNT_SOURCE_INVALID", "source group ids must be positive")
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) > 0 {
		count, err := s.client.Group.Query().Where(group.IDIn(ids...)).Count(ctx)
		if err != nil {
			return nil, fmt.Errorf("validate source groups: %w", err)
		}
		if count != len(ids) {
			return nil, infraerrors.BadRequest("PAID_DISCOUNT_SOURCE_INVALID", "one or more source groups no longer exist")
		}
	}
	cfg.SourceGroupIDs = ids
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	err = s.client.Setting.Create().SetKey(paidRedeemDiscountSettingKey).SetValue(string(encoded)).
		OnConflictColumns(setting.FieldKey).UpdateNewValues().Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("save paid redeem discount: %w", err)
	}
	return &cfg, nil
}

// grantPaidDiscount only lowers an override and leaves RPM and other groups intact.
func grantPaidDiscount(ctx context.Context, client *dbent.Client, userID, groupID int64, rate float64) (bool, error) {
	result, err := client.ExecContext(ctx, `INSERT INTO user_group_rate_multipliers
		(user_id, group_id, rate_multiplier, created_at, updated_at)
		VALUES ($1, $2, $3, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT (user_id, group_id) DO UPDATE
		SET rate_multiplier = EXCLUDED.rate_multiplier, updated_at = CURRENT_TIMESTAMP
		WHERE user_group_rate_multipliers.rate_multiplier IS NULL
		   OR user_group_rate_multipliers.rate_multiplier > EXCLUDED.rate_multiplier`, userID, groupID, rate)
	if err != nil {
		return false, fmt.Errorf("grant paid discount: %w", err)
	}
	changed, err := result.RowsAffected()
	return changed > 0, err
}

// Queue the existing durable auth invalidation events in the same transaction.
// Auth invalidations also clear rate resolvers on every application instance.
func enqueuePaidDiscountInvalidations(ctx context.Context, client *dbent.Client, userIDs []int64) error {
	if len(userIDs) == 0 {
		return nil
	}
	keys, err := client.APIKey.Query().Where(apikey.UserIDIn(userIDs...), apikey.DeletedAtIsNil()).Select(apikey.FieldKey).Strings(ctx)
	if err != nil {
		return fmt.Errorf("load discount invalidation keys: %w", err)
	}
	for _, key := range keys {
		digest := sha256.Sum256([]byte(key))
		if _, err := client.ExecContext(ctx, `INSERT INTO auth_cache_invalidation_outbox (cache_key) VALUES ($1)`, hex.EncodeToString(digest[:])); err != nil {
			return fmt.Errorf("enqueue discount invalidation: %w", err)
		}
	}
	return nil
}

// ApplyRedeem must use the caller's redemption transaction, before it commits.
func (s *PaidRedeemDiscountService) ApplyRedeem(ctx context.Context, client *dbent.Client, userID int64, code *RedeemCode) error {
	if !isPaidDiscountRedeemCode(code) {
		return nil
	}
	cfg, err := loadPaidRedeemDiscountConfig(ctx, client)
	if err != nil || !cfg.Enabled {
		return err
	}
	target, err := paidDiscountTarget(ctx, client, cfg)
	if err != nil {
		return err
	}
	changed, err := grantPaidDiscount(ctx, client, userID, target.ID, math.Min(cfg.RateMultiplier, target.RateMultiplier))
	if err != nil || !changed {
		return err
	}
	return enqueuePaidDiscountInvalidations(ctx, client, []int64{userID})
}

func loadPaidDiscountRates(ctx context.Context, client *dbent.Client, groupID int64) (map[int64]*float64, error) {
	rows, err := client.QueryContext(ctx, `SELECT user_id, rate_multiplier FROM user_group_rate_multipliers WHERE group_id = $1`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rates := make(map[int64]*float64)
	for rows.Next() {
		var id int64
		var rate sql.NullFloat64
		if err := rows.Scan(&id, &rate); err != nil {
			return nil, err
		}
		if rate.Valid {
			value := rate.Float64
			rates[id] = &value
		}
	}
	return rates, rows.Err()
}

func buildPaidDiscountPreview(ctx context.Context, client *dbent.Client) (*PaidRedeemDiscountPreview, error) {
	cfg, err := loadPaidRedeemDiscountConfig(ctx, client)
	if err != nil {
		return nil, err
	}
	if len(cfg.SourceGroupIDs) == 0 {
		return nil, infraerrors.BadRequest("PAID_DISCOUNT_SOURCE_REQUIRED", "select exact source groups before previewing historical users")
	}
	count, err := client.Group.Query().Where(group.IDIn(cfg.SourceGroupIDs...), group.DeletedAtIsNil()).Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("validate historical source groups: %w", err)
	}
	if count != len(cfg.SourceGroupIDs) {
		return nil, infraerrors.BadRequest("PAID_DISCOUNT_SOURCE_INVALID", "one or more source groups no longer exist; review the configuration")
	}
	target, err := paidDiscountTarget(ctx, client, cfg)
	if err != nil {
		return nil, err
	}
	users, err := client.User.Query().Where(user.RoleNEQ(RoleAdmin), user.HasAllowedGroupsWith(group.IDIn(cfg.SourceGroupIDs...), group.DeletedAtIsNil())).Order(dbent.Asc(user.FieldID)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("load historical discount users: %w", err)
	}
	rates, err := loadPaidDiscountRates(ctx, client, target.ID)
	if err != nil {
		return nil, fmt.Errorf("load historical discount rates: %w", err)
	}
	preview := &PaidRedeemDiscountPreview{Config: *cfg, GroupName: target.Name, DefaultRate: target.RateMultiplier, Users: []PaidRedeemDiscountUser{}}
	for _, u := range users {
		current := rates[u.ID]
		effective := target.RateMultiplier
		if current != nil {
			effective = *current
		}
		next := math.Min(cfg.RateMultiplier, math.Min(target.RateMultiplier, effective))
		willChange := current == nil || *current > next
		preview.Users = append(preview.Users, PaidRedeemDiscountUser{UserID: u.ID, Email: u.Email, Username: u.Username, CurrentRate: current, EffectiveRate: effective, NewRate: next, WillChange: willChange})
		if willChange {
			preview.ChangeCount++
		}
	}
	preview.UserCount = len(preview.Users)
	encoded, err := json.Marshal(preview)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(encoded)
	preview.PreviewToken = hex.EncodeToString(digest[:])
	return preview, nil
}

func (s *PaidRedeemDiscountService) Preview(ctx context.Context) (*PaidRedeemDiscountPreview, error) {
	return buildPaidDiscountPreview(ctx, s.client)
}

// Keep the reviewed users eligible through commit. Lock in a stable order:
// settings, groups, users, then membership rows. SQLite test transactions use
// database-level write serialization instead of unsupported row-lock syntax.
func lockPaidDiscountBackfill(ctx context.Context, client *dbent.Client) (map[int64]bool, error) {
	if client.Driver().Dialect() != dialect.Postgres {
		return nil, nil
	}
	if _, err := client.Setting.Query().Where(setting.KeyEQ(paidRedeemDiscountSettingKey)).ForShare().All(ctx); err != nil {
		return nil, err
	}
	cfg, err := loadPaidRedeemDiscountConfig(ctx, client)
	if err != nil {
		return nil, err
	}
	groupIDs := append([]int64{cfg.GroupID}, cfg.SourceGroupIDs...)
	if _, err := client.Group.Query().Where(group.IDIn(groupIDs...)).Order(dbent.Asc(group.FieldID)).ForShare().All(ctx); err != nil {
		return nil, err
	}
	users, err := client.User.Query().Where(user.RoleNEQ(RoleAdmin), user.HasAllowedGroupsWith(group.IDIn(cfg.SourceGroupIDs...), group.DeletedAtIsNil())).
		Order(dbent.Asc(user.FieldID)).ForShare().All(ctx)
	if err != nil {
		return nil, err
	}
	locked := make(map[int64]bool, len(users))
	ids := make([]int64, 0, len(users))
	for _, u := range users {
		locked[u.ID] = true
		ids = append(ids, u.ID)
	}
	if len(ids) > 0 {
		_, err = client.UserAllowedGroup.Query().Where(userallowedgroup.UserIDIn(ids...), userallowedgroup.GroupIDIn(cfg.SourceGroupIDs...)).
			Order(dbent.Asc(userallowedgroup.FieldUserID), dbent.Asc(userallowedgroup.FieldGroupID)).ForShare().All(ctx)
	}
	return locked, err
}

func (s *PaidRedeemDiscountService) Backfill(ctx context.Context, token string) (*PaidRedeemDiscountResult, error) {
	if strings.TrimSpace(token) == "" {
		return nil, infraerrors.BadRequest("PAID_DISCOUNT_PREVIEW_REQUIRED", "preview the users before applying historical discounts")
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	locked, err := lockPaidDiscountBackfill(txCtx, tx.Client())
	if err != nil {
		return nil, err
	}
	preview, err := buildPaidDiscountPreview(txCtx, tx.Client())
	if err != nil {
		return nil, err
	}
	if token != preview.PreviewToken {
		return nil, infraerrors.Conflict("PAID_DISCOUNT_PREVIEW_CHANGED", "configuration, users, or rates changed; refresh the preview before applying")
	}
	for _, u := range preview.Users {
		if locked != nil && !locked[u.UserID] {
			return nil, infraerrors.Conflict("PAID_DISCOUNT_PREVIEW_CHANGED", "user eligibility changed while applying; refresh the preview")
		}
	}
	changedIDs := make([]int64, 0, preview.ChangeCount)
	for _, u := range preview.Users {
		changed, err := grantPaidDiscount(txCtx, tx.Client(), u.UserID, preview.Config.GroupID, u.NewRate)
		if err != nil {
			return nil, err
		}
		if changed {
			changedIDs = append(changedIDs, u.UserID)
		}
	}
	if err := enqueuePaidDiscountInvalidations(txCtx, tx.Client(), changedIDs); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if s.invalidator != nil {
		for _, id := range changedIDs {
			s.invalidator.InvalidateAuthCacheByUserID(ctx, id)
		}
	}
	return &PaidRedeemDiscountResult{UserCount: preview.UserCount, UpdatedCount: len(changedIDs)}, nil
}
