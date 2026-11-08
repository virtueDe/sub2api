package service

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func profitTestTime(hour int) time.Time {
	return time.Date(2026, 11, 8, hour, 0, 0, 0, ImageProfitLocation)
}
func profitTestPrice(id int64, scope string, scopeID int64, size, amount string, hour int) ImageProcurementPrice {
	return ImageProcurementPrice{ID: id, ScopeType: scope, ScopeID: scopeID, Model: "gemini-nano-banana-2.1", Resolution: size, UnitPrice: decimal.RequireFromString(amount), EffectiveAt: profitTestTime(hour)}
}
func profitTestUsage(hour int) ImageProfitUsage {
	return ImageProfitUsage{AccountID: 1, ChannelID: 2, Model: "gemini-nano-banana-2.1", Resolution: "1K", ImageCount: 2, Revenue: decimal.RequireFromString("0.8"), CreatedAt: profitTestTime(hour)}
}

func TestImageProfitPricesUseCallTimeAndAccountOverride(t *testing.T) {
	c := NewImageProfitCalculator([]ImageProcurementPrice{
		profitTestPrice(1, "channel", 2, "1K", "0.1", 0), profitTestPrice(2, "channel", 2, "1K", "0.2", 12),
		profitTestPrice(3, "account", 1, "1K", "0.3", 6), profitTestPrice(4, "account", 1, "1K", "0.25", 15),
	})
	for _, hour := range []int{1, 8, 13, 16} {
		c.Add(profitTestUsage(hour))
	}
	r := c.Report()
	require.Equal(t, "complete", r.Status)
	require.True(t, r.Revenue.Equal(decimal.RequireFromString("3.2")))
	require.True(t, r.KnownCost.Equal(decimal.RequireFromString("1.9")), r.KnownCost.String())
	require.True(t, r.Profit.Equal(decimal.RequireFromString("1.3")))
	require.Equal(t, 8, r.ImageCount)
	require.Equal(t, 4, r.RequestCount)
	require.Len(t, r.Lines[0].Costs, 3)
}

func TestImageProfitMixedSizesDoNotDuplicateRevenue(t *testing.T) {
	c := NewImageProfitCalculator([]ImageProcurementPrice{profitTestPrice(1, "account", 1, "1K", "0.1", 0), profitTestPrice(2, "account", 1, "4K", "0.3", 0)})
	u := profitTestUsage(10)
	u.Resolution = "4K"
	u.Breakdown = map[string]int{"1K": 1, "4K": 1, "image_cache_read_tokens": 99}
	c.Add(u)
	r := c.Report()
	require.Equal(t, 2, r.ImageCount)
	require.True(t, r.Revenue.Equal(decimal.RequireFromString("0.8")))
	require.True(t, r.KnownCost.Equal(decimal.RequireFromString("0.4")))
}

func TestImageProfitMissingDataNeverBecomesZeroCost(t *testing.T) {
	for _, kind := range []string{"missing_price", "unknown_resolution", "unknown_image_count", "inconsistent_image_count"} {
		t.Run(kind, func(t *testing.T) {
			u := profitTestUsage(10)
			switch kind {
			case "unknown_resolution":
				u.Resolution = ""
			case "unknown_image_count":
				u.ImageCount = 0
			case "inconsistent_image_count":
				u.Breakdown = map[string]int{"1K": 1}
			}
			c := NewImageProfitCalculator(nil)
			c.Add(u)
			r := c.Report()
			require.Equal(t, "incomplete", r.Status)
			require.Nil(t, r.Profit)
			require.Equal(t, 1, r.MissingCount)
			require.Nil(t, r.Lines[0].Profit)
			require.Nil(t, r.Lines[0].Costs[0].Cost)
			require.Equal(t, kind, r.Lines[0].Costs[0].MissingReason)
		})
	}
}

func TestImageProfitZeroPriceAndPerCallRounding(t *testing.T) {
	c := NewImageProfitCalculator([]ImageProcurementPrice{profitTestPrice(1, "account", 1, "1K", "0", 0)})
	u := profitTestUsage(1)
	u.Revenue = decimal.RequireFromString("0.000078125")
	c.Add(u)
	c.Add(u)
	r := c.Report()
	require.Equal(t, "complete", r.Status)
	require.True(t, r.Revenue.Equal(decimal.RequireFromString("0.00015626")))
	require.NotNil(t, r.Profit)
}

func TestImageProfitUnconfirmedZeroChargeCannotCreateProcurementLoss(t *testing.T) {
	c := NewImageProfitCalculator([]ImageProcurementPrice{profitTestPrice(1, "account", 1, "1K", "0.1", 0)})
	u := profitTestUsage(1)
	u.Revenue = decimal.Zero
	c.Add(u)
	r := c.Report()
	require.Equal(t, "incomplete", r.Status)
	require.Nil(t, r.Profit)
	require.True(t, r.KnownCost.IsZero())
	require.Equal(t, "unconfirmed_charge", r.Lines[0].Costs[0].MissingReason)
}

func TestImageProfitFutureAndCorrectedPriceVersions(t *testing.T) {
	c := NewImageProfitCalculator([]ImageProcurementPrice{profitTestPrice(1, "account", 1, "1K", "0.1", 10)})
	c.Add(profitTestUsage(9))
	require.Equal(t, "incomplete", c.Report().Status)
	prices := []ImageProcurementPrice{profitTestPrice(1, "account", 1, "1K", "0.1", 0), profitTestPrice(2, "account", 1, "1K", "0.2", 0)}
	c = NewImageProfitCalculator(prices)
	c.Add(profitTestUsage(1))
	require.True(t, c.Report().KnownCost.Equal(decimal.RequireFromString("0.4")))
}

func TestImageProfitDueDateUsesShanghaiSevenAM(t *testing.T) {
	require.Equal(t, "2026-11-06", ImageProfitDueDate(profitTestTime(6).UTC()).Format("2006-01-02"))
	require.Equal(t, "2026-11-07", ImageProfitDueDate(profitTestTime(7).UTC()).Format("2006-01-02"))
	require.Equal(t, "2026-11-07", ImageProfitDueDate(profitTestTime(23).UTC()).Format("2006-01-02"))
}

type imageProfitRepoStub struct {
	ImageProfitRepository
	pending []time.Time
	settled []time.Time
	price   *ImageProcurementPrice
}

func (s *imageProfitRepoStub) PendingDates(_ context.Context, _ time.Time, _ int) ([]time.Time, error) {
	return s.pending, nil
}
func (s *imageProfitRepoStub) Settle(_ context.Context, d time.Time, _ string, _ int64, _ bool) (*ImageProfitReport, error) {
	s.settled = append(s.settled, d)
	return &ImageProfitReport{}, nil
}
func (s *imageProfitRepoStub) CreatePrice(_ context.Context, p *ImageProcurementPrice) error {
	s.price = p
	return nil
}

func TestImageProfitCatchupAndPriceValidation(t *testing.T) {
	repo := &imageProfitRepoStub{pending: []time.Time{profitTestTime(0).AddDate(0, 0, -3), profitTestTime(0).AddDate(0, 0, -1)}}
	s := NewImageProfitService(repo, nil)
	require.NoError(t, s.RunDue(context.Background(), profitTestTime(8)))
	require.Equal(t, repo.pending, repo.settled)
	p := profitTestPrice(0, "account", 1, "1K", "0.123", 0)
	p.Note = "initial"
	p.CreatedBy = 1
	require.NoError(t, s.CreatePrice(context.Background(), &p))
	require.NotNil(t, repo.price)
	p.UnitPrice = decimal.RequireFromString("-0.1")
	require.Error(t, s.CreatePrice(context.Background(), &p))
	p.UnitPrice = decimal.RequireFromString("0.00000000001")
	require.Error(t, s.CreatePrice(context.Background(), &p))
	_, err := s.Recalculate(context.Background(), "2026-01-01", "", 1)
	require.Error(t, err)
	_, err = s.ListReports(context.Background(), "2026-01-02", "2026-01-01", false)
	require.Error(t, err)
}
