package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/ent/redeemcode"
	"github.com/Wei-Shaw/sub2api/ent/user"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	PaidEntitlementProfileDefault = "paid_default"
	paidRedeemMinimumValue        = 10.0
)

type PaidEntitlementGroupPreview struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type PaidEntitlementUserPreview struct {
	UserID                 int64                         `json:"user_id"`
	Email                  string                        `json:"email"`
	Username               string                        `json:"username"`
	RedeemCodeCount        int                           `json:"redeem_code_count"`
	TotalRedeemedAmount    float64                       `json:"total_redeemed_amount"`
	CurrentExclusiveGroups []PaidEntitlementGroupPreview `json:"current_exclusive_groups"`
}

type PaidEntitlementSyncPreview struct {
	Users           []PaidEntitlementUserPreview `json:"users"`
	UserCount       int                          `json:"user_count"`
	UnusedCodeCount int                          `json:"unused_code_count"`
}

type PaidEntitlementSyncResult struct {
	GroupID          int64 `json:"group_id"`
	SyncedUserCount  int   `json:"synced_user_count"`
	UpdatedCodeCount int   `json:"updated_code_count"`
}

type paidEntitlementUserStats struct {
	codeCount int
	total     float64
}

func isPaidRedeemAmount(value float64) bool {
	return value >= paidRedeemMinimumValue && math.Trunc(value) == value
}

// IsPaidBalanceRedeemCode identifies codes that carry the paid entitlement
// snapshot. Historical paid users are intentionally detected separately by
// amount and type because old redeemed codes did not have this snapshot.
func IsPaidBalanceRedeemCode(code *RedeemCode) bool {
	return code != nil &&
		code.Type == RedeemTypeBalance &&
		isPaidRedeemAmount(code.Value) &&
		code.EntitlementProfile != "none" &&
		len(code.EntitlementGroupIDs) > 0
}

func paidEntitlementRedeemCodeQuery(client *dbent.Client, status string, requireSnapshot bool) *dbent.RedeemCodeQuery {
	query := client.RedeemCode.Query().Where(
		redeemcode.TypeEQ(RedeemTypeBalance),
		redeemcode.StatusEQ(status),
		redeemcode.ValueGTE(paidRedeemMinimumValue),
	)
	if status == StatusUsed {
		query = query.Where(redeemcode.UsedByNotNil())
	}
	if requireSnapshot {
		query = query.Where(redeemcode.EntitlementProfileNEQ("none"))
	}
	return query
}

func loadHistoricalPaidUserStats(ctx context.Context, client *dbent.Client) (map[int64]paidEntitlementUserStats, error) {
	codes, err := paidEntitlementRedeemCodeQuery(client, StatusUsed, false).All(ctx)
	if err != nil {
		return nil, err
	}

	stats := make(map[int64]paidEntitlementUserStats)
	for _, code := range codes {
		if code.UsedBy == nil || !isPaidRedeemAmount(code.Value) {
			continue
		}
		item := stats[*code.UsedBy]
		item.codeCount++
		item.total += code.Value
		stats[*code.UsedBy] = item
	}
	return stats, nil
}

func loadUnusedPaidEntitlementCodes(ctx context.Context, client *dbent.Client) ([]*dbent.RedeemCode, error) {
	codes, err := paidEntitlementRedeemCodeQuery(client, StatusUnused, true).All(ctx)
	if err != nil {
		return nil, err
	}

	eligible := make([]*dbent.RedeemCode, 0, len(codes))
	for _, code := range codes {
		if isPaidRedeemAmount(code.Value) && len(code.EntitlementGroupIds) > 0 {
			eligible = append(eligible, code)
		}
	}
	return eligible, nil
}

func paidEntitlementUserIDs(stats map[int64]paidEntitlementUserStats) []int64 {
	ids := make([]int64, 0, len(stats))
	for id := range stats {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (s *adminServiceImpl) PreviewPaidEntitlementSync(ctx context.Context) (*PaidEntitlementSyncPreview, error) {
	if s.entClient == nil {
		return nil, errors.New("paid entitlement preview is not configured")
	}

	stats, err := loadHistoricalPaidUserStats(ctx, s.entClient)
	if err != nil {
		return nil, fmt.Errorf("load historical paid users: %w", err)
	}
	unusedCodes, err := loadUnusedPaidEntitlementCodes(ctx, s.entClient)
	if err != nil {
		return nil, fmt.Errorf("load unused paid entitlement codes: %w", err)
	}

	preview := &PaidEntitlementSyncPreview{
		Users:           []PaidEntitlementUserPreview{},
		UserCount:       len(stats),
		UnusedCodeCount: len(unusedCodes),
	}
	userIDs := paidEntitlementUserIDs(stats)
	if len(userIDs) == 0 {
		return preview, nil
	}

	users, err := s.entClient.User.Query().
		Where(user.IDIn(userIDs...)).
		WithAllowedGroups(func(query *dbent.GroupQuery) {
			query.Where(group.IsExclusiveEQ(true)).Order(dbent.Asc(group.FieldID))
		}).
		Order(dbent.Asc(user.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("load historical paid user details: %w", err)
	}

	preview.Users = make([]PaidEntitlementUserPreview, 0, len(users))
	for _, entity := range users {
		itemStats := stats[entity.ID]
		item := PaidEntitlementUserPreview{
			UserID:                 entity.ID,
			Email:                  entity.Email,
			Username:               entity.Username,
			RedeemCodeCount:        itemStats.codeCount,
			TotalRedeemedAmount:    itemStats.total,
			CurrentExclusiveGroups: []PaidEntitlementGroupPreview{},
		}
		for _, allowedGroup := range entity.Edges.AllowedGroups {
			item.CurrentExclusiveGroups = append(item.CurrentExclusiveGroups, PaidEntitlementGroupPreview{
				ID:     allowedGroup.ID,
				Name:   allowedGroup.Name,
				Status: allowedGroup.Status,
			})
		}
		preview.Users = append(preview.Users, item)
	}
	preview.UserCount = len(preview.Users)
	return preview, nil
}

func validatePaidEntitlementTarget(target *dbent.Group) error {
	if target.Status != StatusActive {
		return infraerrors.BadRequest("PAID_ENTITLEMENT_GROUP_INACTIVE", "paid entitlement group must be active")
	}
	if !target.IsExclusive {
		return infraerrors.BadRequest("PAID_ENTITLEMENT_GROUP_NOT_EXCLUSIVE", "paid entitlement group must be exclusive")
	}
	if target.SubscriptionType == SubscriptionTypeSubscription {
		return infraerrors.BadRequest("PAID_ENTITLEMENT_GROUP_SUBSCRIPTION", "subscription groups cannot be paid entitlement groups")
	}
	return nil
}

func appendUniqueInt64(values []int64, value int64) ([]int64, bool) {
	for _, existing := range values {
		if existing == value {
			return values, false
		}
	}
	return append(append([]int64(nil), values...), value), true
}

func mergeUniqueInt64(values ...[]int64) []int64 {
	seen := make(map[int64]struct{})
	merged := make([]int64, 0)
	for _, list := range values {
		for _, value := range list {
			if value <= 0 {
				continue
			}
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			merged = append(merged, value)
		}
	}
	return merged
}

func activePaidEntitlementGroupIDs(ctx context.Context, client *dbent.Client) ([]int64, error) {
	return client.Group.Query().Where(
		group.IsPaidEntitlementEQ(true),
		group.IsExclusiveEQ(true),
		group.StatusEQ(StatusActive),
		group.SubscriptionTypeEQ(SubscriptionTypeStandard),
	).Order(dbent.Asc(group.FieldID)).IDs(ctx)
}

func (s *adminServiceImpl) ActivatePaidEntitlementGroup(ctx context.Context, id int64) (*PaidEntitlementSyncResult, error) {
	if s.entClient == nil || s.userRepo == nil {
		return nil, errors.New("paid entitlement synchronization is not configured")
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin paid entitlement transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()

	target, err := client.Group.Query().Where(group.IDEQ(id)).ForUpdate().Only(txCtx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, ErrGroupNotFound
		}
		return nil, fmt.Errorf("load paid entitlement group: %w", err)
	}
	if err := validatePaidEntitlementTarget(target); err != nil {
		return nil, err
	}

	stats, err := loadHistoricalPaidUserStats(txCtx, client)
	if err != nil {
		return nil, fmt.Errorf("load historical paid users: %w", err)
	}
	userIDs := paidEntitlementUserIDs(stats)
	for _, userID := range userIDs {
		if err := s.userRepo.AddGroupToAllowedGroups(txCtx, userID, id); err != nil {
			return nil, fmt.Errorf("grant paid entitlement group to user %d: %w", userID, err)
		}
	}

	unusedCodes, err := loadUnusedPaidEntitlementCodes(txCtx, client)
	if err != nil {
		return nil, fmt.Errorf("load unused paid entitlement codes: %w", err)
	}
	updatedCodeCount := 0
	for _, code := range unusedCodes {
		groupIDs, changed := appendUniqueInt64(code.EntitlementGroupIds, id)
		if !changed && code.EntitlementProfile == PaidEntitlementProfileDefault {
			continue
		}
		if _, err := client.RedeemCode.UpdateOneID(code.ID).
			SetEntitlementProfile(PaidEntitlementProfileDefault).
			SetEntitlementGroupIds(groupIDs).
			Save(txCtx); err != nil {
			return nil, fmt.Errorf("update paid entitlement redeem code %d: %w", code.ID, err)
		}
		updatedCodeCount++
	}

	if _, err := client.Group.UpdateOneID(id).SetIsPaidEntitlement(true).Save(txCtx); err != nil {
		return nil, fmt.Errorf("activate paid entitlement group: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit paid entitlement synchronization: %w", err)
	}

	if s.authCacheInvalidator != nil {
		for _, userID := range userIDs {
			s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
		}
	}

	return &PaidEntitlementSyncResult{
		GroupID:          id,
		SyncedUserCount:  len(userIDs),
		UpdatedCodeCount: updatedCodeCount,
	}, nil
}
