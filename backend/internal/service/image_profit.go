package service

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	appErrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

// This ledger uses usage-log fees, never the current selling price or balance.
// Procurement rules and reports are append-only so later edits cannot reprice history.
type ImageProcurementPrice struct {
	ID          int64           `json:"id"`
	ScopeType   string          `json:"scope_type"`
	ScopeID     int64           `json:"scope_id"`
	ScopeName   string          `json:"scope_name"`
	Model       string          `json:"model"`
	Resolution  string          `json:"resolution"`
	UnitPrice   decimal.Decimal `json:"unit_price_usd"`
	EffectiveAt time.Time       `json:"effective_at"`
	Note        string          `json:"note"`
	CreatedBy   int64           `json:"created_by"`
	CreatedAt   time.Time       `json:"created_at"`
}

type ImageProfitUsage struct {
	ID                                          int64
	AccountID, ChannelID                        int64
	AccountName, ChannelName, Model, Resolution string
	ImageCount                                  int
	Breakdown                                   map[string]int
	Revenue                                     decimal.Decimal
	CreatedAt                                   time.Time
}

type ImageProfitCost struct {
	Resolution    string           `json:"resolution"`
	ImageCount    int              `json:"image_count"`
	PriceID       int64            `json:"price_id"`
	UnitPrice     *decimal.Decimal `json:"unit_price_usd"`
	Cost          *decimal.Decimal `json:"cost_usd"`
	MissingReason string           `json:"missing_reason,omitempty"`
}

type ImageProfitLine struct {
	AccountID    int64              `json:"account_id"`
	ChannelID    int64              `json:"channel_id"`
	AccountName  string             `json:"account_name"`
	ChannelName  string             `json:"channel_name"`
	Model        string             `json:"model"`
	RequestCount int                `json:"request_count"`
	ImageCount   int                `json:"image_count"`
	Revenue      decimal.Decimal    `json:"revenue_usd"`
	KnownCost    decimal.Decimal    `json:"known_cost_usd"`
	MissingCount int                `json:"missing_count"`
	Profit       *decimal.Decimal   `json:"profit_usd"`
	Costs        []*ImageProfitCost `json:"costs"`
}

type ImageProfitReport struct {
	SourceIDs        []int64            `json:"source_ids,omitempty"`
	SourceIncomplete bool               `json:"source_incomplete"`
	ID               int64              `json:"id"`
	Date             string             `json:"date"`
	Revision         int                `json:"revision"`
	Status           string             `json:"status"`
	RequestCount     int                `json:"request_count"`
	ImageCount       int                `json:"image_count"`
	Revenue          decimal.Decimal    `json:"revenue_usd"`
	KnownCost        decimal.Decimal    `json:"known_cost_usd"`
	MissingCount     int                `json:"missing_count"`
	Profit           *decimal.Decimal   `json:"profit_usd"`
	Reason           string             `json:"reason"`
	CreatedBy        int64              `json:"created_by"`
	CreatedAt        time.Time          `json:"created_at"`
	Lines            []*ImageProfitLine `json:"lines,omitempty"`
}

type ImageProfitRepository interface {
	ListPrices(context.Context, int, int) ([]ImageProcurementPrice, int, error)
	CreatePrice(context.Context, *ImageProcurementPrice) error
	ListReports(context.Context, string, string, bool) ([]ImageProfitReport, error)
	GetReport(context.Context, int64) (*ImageProfitReport, error)
	Settle(context.Context, time.Time, string, int64, bool) (*ImageProfitReport, error)
	PendingDates(context.Context, time.Time, int) ([]time.Time, error)
}

func normalizeProfitModel(model string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(model)), "models/")
}

// Match a call's immutable dimensions against the price effective at that call.
// Account overrides win even when the channel default was updated more recently.
type ImageProfitCalculator struct {
	sourceIDs []int64
	rules     map[string][]ImageProcurementPrice
	lines     map[string]*ImageProfitLine
	costs     map[string]map[string]*ImageProfitCost
}

func NewImageProfitCalculator(prices []ImageProcurementPrice) *ImageProfitCalculator {
	c := &ImageProfitCalculator{rules: map[string][]ImageProcurementPrice{}, lines: map[string]*ImageProfitLine{}, costs: map[string]map[string]*ImageProfitCost{}}
	for _, p := range prices {
		key := profitRuleKey(p.ScopeType, p.ScopeID, p.Model, p.Resolution)
		c.rules[key] = append(c.rules[key], p)
	}
	for _, rules := range c.rules {
		sort.Slice(rules, func(i, j int) bool {
			if rules[i].EffectiveAt.Equal(rules[j].EffectiveAt) {
				return rules[i].ID > rules[j].ID
			}
			return rules[i].EffectiveAt.After(rules[j].EffectiveAt)
		})
	}
	return c
}

func profitRuleKey(scope string, id int64, model, resolution string) string {
	return fmt.Sprintf("%s:%d:%s:%s", scope, id, normalizeProfitModel(model), resolution)
}

func (c *ImageProfitCalculator) price(u ImageProfitUsage, resolution string) *ImageProcurementPrice {
	for _, key := range []string{profitRuleKey("account", u.AccountID, u.Model, resolution), profitRuleKey("channel", u.ChannelID, u.Model, resolution)} {
		for _, p := range c.rules[key] {
			if !p.EffectiveAt.After(u.CreatedAt) {
				return &p
			}
		}
	}
	return nil
}

func profitImageSizes(u ImageProfitUsage) (map[string]int, string) {
	if u.ImageCount <= 0 {
		return nil, "unknown_image_count"
	}
	sizes := map[string]int{}
	count := 0
	for _, size := range []string{"1K", "2K", "4K"} {
		if n := u.Breakdown[size]; n > 0 {
			sizes[size] = n
			count += n
		}
	}
	if count > 0 {
		if count != u.ImageCount {
			return nil, "inconsistent_image_count"
		}
		return sizes, ""
	}
	if size, ok := ClassifyImageBillingTier(u.Resolution); ok {
		return map[string]int{size: u.ImageCount}, ""
	}
	return nil, "unknown_resolution"
}

func (c *ImageProfitCalculator) Add(u ImageProfitUsage) {
	if u.ID > 0 {
		c.sourceIDs = append(c.sourceIDs, u.ID)
	}
	u.Model = normalizeProfitModel(u.Model)
	key := fmt.Sprintf("%d:%d:%s", u.AccountID, u.ChannelID, u.Model)
	line := c.lines[key]
	if line == nil {
		line = &ImageProfitLine{AccountID: u.AccountID, ChannelID: u.ChannelID, AccountName: u.AccountName, ChannelName: u.ChannelName, Model: u.Model, Costs: []*ImageProfitCost{}}
		c.lines[key] = line
		c.costs[key] = map[string]*ImageProfitCost{}
	}
	line.RequestCount++
	line.ImageCount += max(u.ImageCount, 0)
	line.Revenue = line.Revenue.Add(u.Revenue.Round(UsageBillingMonetaryScale))
	sizes, missing := profitImageSizes(u)
	// Historical usage rows cannot distinguish free output from failed billing.
	// Do not invent procurement charges or a complete profit for those rows.
	if !u.Revenue.IsPositive() {
		missing = "unconfirmed_charge"
	}
	if missing != "" {
		sizes = map[string]int{"unknown": max(u.ImageCount, 0)}
	}
	missingCall := false
	for size, count := range sizes {
		price := c.price(u, size)
		reason := missing
		if reason == "" && price == nil {
			reason = "missing_price"
		}
		priceID := int64(0)
		if reason == "" {
			priceID = price.ID
		}
		costKey := fmt.Sprintf("%s:%d:%s", size, priceID, reason)
		cost := c.costs[key][costKey]
		if cost == nil {
			cost = &ImageProfitCost{Resolution: size, PriceID: priceID, MissingReason: reason}
			c.costs[key][costKey] = cost
			line.Costs = append(line.Costs, cost)
			if reason == "" {
				unit := price.UnitPrice
				total := decimal.Zero
				cost.UnitPrice = &unit
				cost.Cost = &total
			}
		}
		cost.ImageCount += count
		if reason != "" {
			missingCall = true
			continue
		}
		amount := price.UnitPrice.Mul(decimal.NewFromInt(int64(count)))
		total := cost.Cost.Add(amount)
		cost.Cost = &total
		line.KnownCost = line.KnownCost.Add(amount)
	}
	if missingCall {
		line.MissingCount++
	}
}

func (c *ImageProfitCalculator) Report() ImageProfitReport {
	r := ImageProfitReport{Status: "complete", Lines: []*ImageProfitLine{}, SourceIDs: c.sourceIDs}
	for _, line := range c.lines {
		r.RequestCount += line.RequestCount
		r.ImageCount += line.ImageCount
		r.Revenue = r.Revenue.Add(line.Revenue)
		r.KnownCost = r.KnownCost.Add(line.KnownCost)
		r.MissingCount += line.MissingCount
		if line.MissingCount == 0 {
			profit := line.Revenue.Sub(line.KnownCost)
			line.Profit = &profit
		}
		sort.Slice(line.Costs, func(i, j int) bool {
			a, b := line.Costs[i], line.Costs[j]
			if a.Resolution != b.Resolution {
				return a.Resolution < b.Resolution
			}
			return a.PriceID < b.PriceID
		})
		r.Lines = append(r.Lines, line)
	}
	sort.Slice(r.Lines, func(i, j int) bool {
		a, b := r.Lines[i], r.Lines[j]
		if a.AccountID != b.AccountID {
			return a.AccountID < b.AccountID
		}
		if a.ChannelID != b.ChannelID {
			return a.ChannelID < b.ChannelID
		}
		return a.Model < b.Model
	})
	if r.MissingCount > 0 {
		r.Status = "incomplete"
	} else {
		profit := r.Revenue.Sub(r.KnownCost)
		r.Profit = &profit
	}
	return r
}

type ImageProfitService struct {
	repo    ImageProfitRepository
	enabled bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// Fixed UTC+08:00 is deliberately independent of server/browser timezone.
var ImageProfitLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

func NewImageProfitService(repo ImageProfitRepository, cfg *config.Config) *ImageProfitService {
	return &ImageProfitService{repo: repo, enabled: cfg == nil || cfg.RunMode != config.RunModeSimple}
}

func ProvideImageProfitService(repo ImageProfitRepository, cfg *config.Config) (*ImageProfitService, func()) {
	s := NewImageProfitService(repo, cfg)
	s.Start()
	return s, s.Stop
}

func (s *ImageProfitService) ListPrices(ctx context.Context, page, size int) ([]ImageProcurementPrice, int, error) {
	return s.repo.ListPrices(ctx, page, size)
}
func (s *ImageProfitService) GetReport(ctx context.Context, id int64) (*ImageProfitReport, error) {
	return s.repo.GetReport(ctx, id)
}

func (s *ImageProfitService) CreatePrice(ctx context.Context, p *ImageProcurementPrice) error {
	p.Model = normalizeProfitModel(p.Model)
	p.Resolution = strings.ToUpper(strings.TrimSpace(p.Resolution))
	p.Note = strings.TrimSpace(p.Note)
	if (p.ScopeType != "account" && p.ScopeType != "channel") || p.ScopeID <= 0 || p.Model == "" || len(p.Model) > 200 || (p.Resolution != "1K" && p.Resolution != "2K" && p.Resolution != "4K") || p.UnitPrice.IsNegative() || p.UnitPrice.GreaterThanOrEqual(decimal.NewFromInt(10000000000)) || !p.UnitPrice.Equal(p.UnitPrice.Round(10)) || len(p.Note) == 0 || len(p.Note) > 500 || p.CreatedBy <= 0 {
		return appErrors.BadRequest("INVALID_PROCUREMENT_PRICE", "Specify account/channel, model, resolution, non-negative USD price (up to 10 decimals) and a note")
	}
	if p.EffectiveAt.IsZero() {
		p.EffectiveAt = time.Now()
	}
	return s.repo.CreatePrice(ctx, p)
}

func profitDay(value string) (time.Time, error) {
	d, err := time.ParseInLocation("2006-01-02", value, ImageProfitLocation)
	if err != nil {
		return time.Time{}, appErrors.BadRequest("INVALID_REPORT_DATE", "Use YYYY-MM-DD")
	}
	return d, nil
}

func (s *ImageProfitService) ListReports(ctx context.Context, start, end string, history bool) ([]ImageProfitReport, error) {
	a, err := profitDay(start)
	if err != nil {
		return nil, err
	}
	b, err := profitDay(end)
	if err != nil {
		return nil, err
	}
	if b.Before(a) || b.Sub(a) > 366*24*time.Hour {
		return nil, appErrors.BadRequest("INVALID_REPORT_RANGE", "Select up to 366 days")
	}
	if history && start != end {
		return nil, appErrors.BadRequest("INVALID_HISTORY_DATE", "Select one day for revision history")
	}
	return s.repo.ListReports(ctx, start, end, history)
}

func (s *ImageProfitService) Recalculate(ctx context.Context, date, reason string, actor int64) (*ImageProfitReport, error) {
	if !s.enabled {
		return nil, appErrors.BadRequest("PROFIT_UNAVAILABLE", "Profit accounting requires billing mode")
	}
	day, err := profitDay(date)
	if err != nil {
		return nil, err
	}
	now := time.Now().In(ImageProfitLocation)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, ImageProfitLocation)
	reason = strings.TrimSpace(reason)
	if !day.Before(today) || reason == "" || len(reason) > 500 || actor <= 0 {
		return nil, appErrors.BadRequest("INVALID_RECALCULATION", "Select a completed day and provide a recalculation reason")
	}
	return s.repo.Settle(ctx, day, reason, actor, true)
}

func ImageProfitDueDate(now time.Time) time.Time {
	now = now.In(ImageProfitLocation)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, ImageProfitLocation).AddDate(0, 0, -1)
	if now.Hour() < 7 {
		day = day.AddDate(0, 0, -1)
	}
	return day
}

func (s *ImageProfitService) RunDue(ctx context.Context, now time.Time) error {
	if !s.enabled {
		return nil
	}
	dates, err := s.repo.PendingDates(ctx, ImageProfitDueDate(now), 14)
	if err != nil {
		return err
	}
	for _, day := range dates {
		if _, err = s.repo.Settle(ctx, day, "scheduled", 0, false); err != nil {
			return err
		}
	}
	return nil
}

func (s *ImageProfitService) Start() {
	if !s.enabled {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			runCtx, done := context.WithTimeout(ctx, 5*time.Minute)
			err := s.RunDue(runCtx, time.Now())
			done()
			if err != nil && ctx.Err() == nil {
				log.Printf("[ImageProfit] settlement failed: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (s *ImageProfitService) Stop() {
	if s.cancel != nil {
		s.cancel()
		s.wg.Wait()
	}
}
