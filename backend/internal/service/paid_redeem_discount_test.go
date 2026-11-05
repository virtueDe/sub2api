//go:build unit

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/redeemcode"
	"github.com/stretchr/testify/require"
)

func newPaidDiscountTestService(t *testing.T) (*PaidRedeemDiscountService, *dbent.Group) {
	t.Helper()
	client := newPaymentConfigServiceTestClient(t)
	_, err := client.ExecContext(context.Background(), `CREATE TABLE user_group_rate_multipliers (
		user_id BIGINT NOT NULL REFERENCES users(id), group_id BIGINT NOT NULL REFERENCES groups(id),
		rate_multiplier NUMERIC NULL, rpm_override INTEGER NULL, created_at TIMESTAMP, updated_at TIMESTAMP,
		PRIMARY KEY (user_id, group_id))`)
	require.NoError(t, err)
	_, err = client.ExecContext(context.Background(), `CREATE TABLE auth_cache_invalidation_outbox (id INTEGER PRIMARY KEY, cache_key TEXT NOT NULL)`)
	require.NoError(t, err)
	target := client.Group.Create().SetName("image target").SetAllowImageGeneration(true).SaveX(context.Background())
	return NewPaidRedeemDiscountService(client, nil), target
}

func paidDiscountTestUser(t *testing.T, client *dbent.Client, name, role string, groups ...int64) *dbent.User {
	t.Helper()
	u := client.User.Create().SetEmail(name + "@example.com").SetPasswordHash("test-only").SetRole(role).SaveX(context.Background())
	for _, id := range groups {
		client.UserAllowedGroup.Create().SetUserID(u.ID).SetGroupID(id).SaveX(context.Background())
	}
	return u
}

func TestPaidRedeemDiscountEligibility(t *testing.T) {
	for _, amount := range []float64{0.01, 1, 9, 10.5, 100} {
		require.True(t, isPaidDiscountRedeemCode(&RedeemCode{Type: RedeemTypeBalance, Value: amount, EntitlementProfile: PaidEntitlementProfileDefault}))
	}
	for _, code := range []*RedeemCode{
		nil,
		{Type: RedeemTypeBalance, Value: 10, EntitlementProfile: "none"},
		{Type: RedeemTypeBalance, Value: 10, EntitlementProfile: "custom"},
		{Type: RedeemTypeBalance, Value: 10},
		{Type: RedeemTypeSubscription, Value: 10, EntitlementProfile: PaidEntitlementProfileDefault},
		{Type: RedeemTypeBalance, Value: 0, EntitlementProfile: PaidEntitlementProfileDefault},
		{Type: RedeemTypeBalance, Value: -1, EntitlementProfile: PaidEntitlementProfileDefault},
		{Type: RedeemTypeBalance, Value: math.NaN(), EntitlementProfile: PaidEntitlementProfileDefault},
	} {
		require.False(t, isPaidDiscountRedeemCode(code))
	}
}

func TestPaidRedeemDiscountConfig(t *testing.T) {
	svc, target := newPaidDiscountTestService(t)
	ctx := context.Background()
	initial, err := svc.GetConfig(ctx)
	require.NoError(t, err)
	require.False(t, initial.Enabled)
	require.Empty(t, initial.SourceGroupIDs)
	source := svc.client.Group.Create().SetName("source").SaveX(ctx)
	cfg := PaidRedeemDiscountConfig{Enabled: true, GroupID: target.ID, RateMultiplier: 0.01, SourceGroupIDs: []int64{source.ID, source.ID}}
	saved, err := svc.UpdateConfig(ctx, cfg)
	require.NoError(t, err)
	require.Equal(t, []int64{source.ID}, saved.SourceGroupIDs)
	loaded, err := svc.GetConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, saved, loaded)
	for _, rate := range []float64{0, -1, 1.1, 0.00001, 0.01001, math.Inf(1), math.NaN()} {
		bad := cfg
		bad.RateMultiplier = rate
		_, err := svc.UpdateConfig(ctx, bad)
		require.Error(t, err)
	}
	svc.client.Group.UpdateOneID(target.ID).SetImageRateIndependent(true).SaveX(ctx)
	_, err = svc.UpdateConfig(ctx, cfg)
	require.ErrorContains(t, err, "shared group multiplier")
	loaded, err = svc.GetConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, saved, loaded, "rejected updates must not replace saved settings")
}

func TestPaidRedeemDiscountBackfillExactGroupsAndPreservesRates(t *testing.T) {
	svc, target := newPaidDiscountTestService(t)
	ctx := context.Background()
	sourceA := svc.client.Group.Create().SetName("paid group").SaveX(ctx)
	sourceB := svc.client.Group.Create().SetName("second paid group").SaveX(ctx)
	lookalike := svc.client.Group.Create().SetName("paid group suffix").SaveX(ctx)
	u1 := paidDiscountTestUser(t, svc.client, "overlapping", RoleUser, sourceA.ID, sourceB.ID)
	u2 := paidDiscountTestUser(t, svc.client, "lower-rate", RoleUser, sourceB.ID)
	outsider := paidDiscountTestUser(t, svc.client, "not-selected", RoleUser, lookalike.ID)
	paidDiscountTestUser(t, svc.client, "admin", RoleAdmin, sourceA.ID)
	deleted := paidDiscountTestUser(t, svc.client, "deleted", RoleUser, sourceA.ID)
	svc.client.User.UpdateOneID(deleted.ID).SetDeletedAt(time.Now()).SaveX(ctx)
	_, err := svc.client.ExecContext(ctx, `INSERT INTO user_group_rate_multipliers (user_id,group_id,rate_multiplier,rpm_override) VALUES ($1,$2,NULL,20),($3,$2,0.005,9),($4,$2,0.5,10)`, u1.ID, target.ID, u2.ID, outsider.ID)
	require.NoError(t, err)
	svc.client.APIKey.Create().SetUserID(u1.ID).SetGroupID(target.ID).SetName("test").SetKey("test-discount-key").SaveX(ctx)
	_, err = svc.UpdateConfig(ctx, PaidRedeemDiscountConfig{Enabled: true, GroupID: target.ID, RateMultiplier: 0.01, SourceGroupIDs: []int64{sourceA.ID, sourceB.ID}})
	require.NoError(t, err)
	preview, err := svc.Preview(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, preview.UserCount)
	require.Equal(t, 1, preview.ChangeCount)
	require.Equal(t, u1.ID, preview.Users[0].UserID)
	require.Equal(t, 0.005, preview.Users[1].NewRate)
	result, err := svc.Backfill(ctx, preview.PreviewToken)
	require.NoError(t, err)
	require.Equal(t, 1, result.UpdatedCount)
	rates, err := loadPaidDiscountRates(ctx, svc.client, target.ID)
	require.NoError(t, err)
	require.InDelta(t, 0.01, *rates[u1.ID], 1e-9)
	require.InDelta(t, 0.005, *rates[u2.ID], 1e-9)
	require.InDelta(t, 0.5, *rates[outsider.ID], 1e-9)
	rows, err := svc.client.QueryContext(ctx, `SELECT rpm_override FROM user_group_rate_multipliers WHERE user_id=$1 AND group_id=$2`, u1.ID, target.ID)
	require.NoError(t, err)
	require.True(t, rows.Next())
	var rpm int
	require.NoError(t, rows.Scan(&rpm))
	require.NoError(t, rows.Close())
	require.Equal(t, 20, rpm)
	rows, err = svc.client.QueryContext(ctx, `SELECT cache_key FROM auth_cache_invalidation_outbox`)
	require.NoError(t, err)
	require.True(t, rows.Next())
	var cacheKey string
	require.NoError(t, rows.Scan(&cacheKey))
	require.NoError(t, rows.Close())
	digest := sha256.Sum256([]byte("test-discount-key"))
	require.Equal(t, hex.EncodeToString(digest[:]), cacheKey)
	preview, err = svc.Preview(ctx)
	require.NoError(t, err)
	require.Zero(t, preview.ChangeCount)
	result, err = svc.Backfill(ctx, preview.PreviewToken)
	require.NoError(t, err)
	require.Zero(t, result.UpdatedCount)
}

func TestPaidRedeemDiscountBackfillRejectsChangedPreview(t *testing.T) {
	for _, change := range []string{"members", "settings", "rates"} {
		t.Run(change, func(t *testing.T) {
			svc, target := newPaidDiscountTestService(t)
			ctx := context.Background()
			source := svc.client.Group.Create().SetName("source").SaveX(ctx)
			u := paidDiscountTestUser(t, svc.client, "existing", RoleUser, source.ID)
			cfg := PaidRedeemDiscountConfig{GroupID: target.ID, RateMultiplier: 0.01, SourceGroupIDs: []int64{source.ID}}
			_, err := svc.UpdateConfig(ctx, cfg)
			require.NoError(t, err)
			preview, err := svc.Preview(ctx)
			require.NoError(t, err)
			switch change {
			case "members":
				paidDiscountTestUser(t, svc.client, "new", RoleUser, source.ID)
			case "settings":
				cfg.RateMultiplier = 0.02
				_, err = svc.UpdateConfig(ctx, cfg)
				require.NoError(t, err)
			case "rates":
				_, err = grantPaidDiscount(ctx, svc.client, u.ID, target.ID, 0.005)
				require.NoError(t, err)
			}
			_, err = svc.Backfill(ctx, preview.PreviewToken)
			require.ErrorContains(t, err, "refresh the preview")
		})
	}
}

func TestPaidRedeemDiscountRejectsDeletedSourceGroup(t *testing.T) {
	svc, target := newPaidDiscountTestService(t)
	ctx := context.Background()
	source := svc.client.Group.Create().SetName("source").SaveX(ctx)
	paidDiscountTestUser(t, svc.client, "historical", RoleUser, source.ID)
	_, err := svc.UpdateConfig(ctx, PaidRedeemDiscountConfig{GroupID: target.ID, RateMultiplier: 0.01, SourceGroupIDs: []int64{source.ID}})
	require.NoError(t, err)
	preview, err := svc.Preview(ctx)
	require.NoError(t, err)
	svc.client.Group.UpdateOneID(source.ID).SetDeletedAt(time.Now()).SaveX(ctx)
	_, err = svc.Preview(ctx)
	require.ErrorContains(t, err, "source groups no longer exist")
	_, err = svc.Backfill(ctx, preview.PreviewToken)
	require.ErrorContains(t, err, "source groups no longer exist")
	rates, err := loadPaidDiscountRates(ctx, svc.client, target.ID)
	require.NoError(t, err)
	require.Empty(t, rates)
}

type paidDiscountRedeemRepo struct {
	RedeemCodeRepository
	client *dbent.Client
}

func (r *paidDiscountRedeemRepo) GetByCode(ctx context.Context, code string) (*RedeemCode, error) {
	e, err := r.client.RedeemCode.Query().Where(redeemcode.CodeEQ(code)).Only(ctx)
	if err != nil {
		return nil, err
	}
	return &RedeemCode{ID: e.ID, Code: e.Code, Type: e.Type, Value: e.Value, Status: e.Status, EntitlementProfile: e.EntitlementProfile}, nil
}

func (r *paidDiscountRedeemRepo) GetByID(ctx context.Context, id int64) (*RedeemCode, error) {
	e, err := r.client.RedeemCode.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return r.GetByCode(ctx, e.Code)
}

func (r *paidDiscountRedeemRepo) Use(ctx context.Context, id, userID int64) error {
	count, err := dbent.TxFromContext(ctx).Client().RedeemCode.Update().Where(redeemcode.IDEQ(id), redeemcode.StatusEQ(StatusUnused)).SetStatus(StatusUsed).SetUsedBy(userID).SetUsedAt(time.Now()).Save(ctx)
	if err == nil && count == 0 {
		return ErrRedeemCodeUsed
	}
	return err
}

func TestPaidRedeemDiscountRedeemTransaction(t *testing.T) {
	for _, mode := range []string{"small_paid", "fractional_paid", "gift", "custom_profile", "disabled", "outbox_failure"} {
		t.Run(mode, func(t *testing.T) {
			svc, target := newPaidDiscountTestService(t)
			ctx := context.Background()
			u := paidDiscountTestUser(t, svc.client, "redeemer", RoleUser)
			svc.client.APIKey.Create().SetUserID(u.ID).SetName("test").SetKey("test-key").SaveX(ctx)
			cfg := PaidRedeemDiscountConfig{Enabled: mode != "disabled", GroupID: target.ID, RateMultiplier: 0.01}
			_, err := svc.UpdateConfig(ctx, cfg)
			require.NoError(t, err)
			profile, amount := PaidEntitlementProfileDefault, 1.0
			if mode == "gift" {
				profile = "none"
			}
			if mode == "custom_profile" {
				profile = "custom"
			}
			if mode == "fractional_paid" {
				amount = 0.5
			}
			code := svc.client.RedeemCode.Create().SetCode("paid-code").SetType(RedeemTypeBalance).SetValue(amount).SetEntitlementProfile(profile).SaveX(ctx)
			if mode == "outbox_failure" {
				_, err = svc.client.ExecContext(ctx, `CREATE TRIGGER reject_discount_event BEFORE INSERT ON auth_cache_invalidation_outbox BEGIN SELECT RAISE(ABORT, 'test outbox failure'); END`)
				require.NoError(t, err)
			}
			users := &mockUserRepo{getByIDUser: &User{ID: u.ID}}
			users.updateBalanceFn = func(ctx context.Context, id int64, amount float64) error {
				return dbent.TxFromContext(ctx).Client().User.UpdateOneID(id).AddBalance(amount).Exec(ctx)
			}
			redeemer := NewRedeemService(&paidDiscountRedeemRepo{client: svc.client}, users, nil, nil, nil, svc.client, nil, nil)
			redeemer.SetPaidRedeemDiscountService(svc)
			_, err = redeemer.Redeem(ctx, u.ID, code.Code)
			if mode == "outbox_failure" {
				require.ErrorContains(t, err, "test outbox failure")
				require.Equal(t, StatusUnused, svc.client.RedeemCode.GetX(ctx, code.ID).Status)
				require.Zero(t, svc.client.User.GetX(ctx, u.ID).Balance)
				rates, rateErr := loadPaidDiscountRates(ctx, svc.client, target.ID)
				require.NoError(t, rateErr)
				require.Empty(t, rates)
				return
			}
			require.NoError(t, err)
			require.InDelta(t, amount, svc.client.User.GetX(ctx, u.ID).Balance, 1e-9)
			rates, err := loadPaidDiscountRates(ctx, svc.client, target.ID)
			require.NoError(t, err)
			if mode == "small_paid" || mode == "fractional_paid" {
				require.NotNil(t, rates[u.ID])
				require.InDelta(t, 0.01, *rates[u.ID], 1e-9)
			} else {
				require.Empty(t, rates)
			}
			_, err = redeemer.Redeem(ctx, u.ID, code.Code)
			require.ErrorIs(t, err, ErrRedeemCodeUsed, fmt.Sprintf("mode=%s", mode))
			require.InDelta(t, amount, svc.client.User.GetX(ctx, u.ID).Balance, 1e-9)
		})
	}
}
