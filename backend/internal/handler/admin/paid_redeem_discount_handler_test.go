//go:build unit

package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func discountHandlerRequest(t *testing.T, handler gin.HandlerFunc, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, "/settings/paid-redeem-discount", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	handler(c)
	return recorder
}

func TestPaidDiscountHandlerRequiresPreviewToken(t *testing.T) {
	for _, body := range []string{`{}`, `{"preview_token":""}`, `{`} {
		response := discountHandlerRequest(t, (&SettingHandler{}).BackfillPaidRedeemDiscount, "POST", body)
		require.Equal(t, 400, response.Code)
	}
}

func TestPaidDiscountHandlerConfigAndStalePreview(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = client.Close() })
	_, err = db.Exec(`CREATE TABLE user_group_rate_multipliers (user_id BIGINT, group_id BIGINT, rate_multiplier NUMERIC, rpm_override INTEGER);
		CREATE TABLE auth_cache_invalidation_outbox (cache_key TEXT);`)
	require.NoError(t, err)
	target := client.Group.Create().SetName("target").SetAllowImageGeneration(true).SaveX(ctx)
	source := client.Group.Create().SetName("source").SaveX(ctx)
	u := client.User.Create().SetEmail("paid@example.com").SetPasswordHash("test").SaveX(ctx)
	client.UserAllowedGroup.Create().SetUserID(u.ID).SetGroupID(source.ID).SaveX(ctx)
	client.APIKey.Create().SetUserID(u.ID).SetName("key").SetKey("test-only").SaveX(ctx)
	_, err = db.Exec(`INSERT INTO user_group_rate_multipliers VALUES ($1,$2,0.5,20)`, u.ID, target.ID)
	require.NoError(t, err)
	h := &SettingHandler{paidDiscountService: service.NewPaidRedeemDiscountService(client, nil)}
	response := discountHandlerRequest(t, h.GetPaidRedeemDiscount, "GET", "")
	require.Equal(t, 200, response.Code)
	cfg := service.PaidRedeemDiscountConfig{Enabled: true, GroupID: target.ID, RateMultiplier: 0.01, SourceGroupIDs: []int64{source.ID}}
	save := func() {
		body, err := json.Marshal(cfg)
		require.NoError(t, err)
		response := discountHandlerRequest(t, h.UpdatePaidRedeemDiscount, "PUT", string(body))
		require.Equal(t, 200, response.Code, response.Body.String())
	}
	save()
	response = discountHandlerRequest(t, h.PreviewPaidRedeemDiscount, "POST", "")
	require.Equal(t, 200, response.Code, response.Body.String())
	var envelope struct {
		Data service.PaidRedeemDiscountPreview `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
	require.NotEmpty(t, envelope.Data.PreviewToken)
	cfg.RateMultiplier = 0.02
	save()
	response = discountHandlerRequest(t, h.BackfillPaidRedeemDiscount, "POST", `{"preview_token":"`+envelope.Data.PreviewToken+`"}`)
	require.Equal(t, 409, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "PAID_DISCOUNT_PREVIEW_CHANGED")
	response = discountHandlerRequest(t, h.BackfillPaidRedeemDiscount, "POST", `{"preview_token":" "}`)
	require.Equal(t, 400, response.Code)
	var rate float64
	var rpm, events int
	require.NoError(t, db.QueryRow(`SELECT rate_multiplier,rpm_override FROM user_group_rate_multipliers`).Scan(&rate, &rpm))
	require.Equal(t, 0.5, rate)
	require.Equal(t, 20, rpm)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM auth_cache_invalidation_outbox`).Scan(&events))
	require.Zero(t, events)
	require.Equal(t, 1, client.UserAllowedGroup.Query().CountX(ctx))
}
