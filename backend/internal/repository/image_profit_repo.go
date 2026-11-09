package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	appErrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type imageProfitRepository struct{ db *sql.DB }

func NewImageProfitRepository(db *sql.DB) service.ImageProfitRepository {
	return &governedImageProfitRepository{imageProfitRepository: &imageProfitRepository{db: db}}
}

const procurementPriceColumns = `p.id,p.scope_type,p.scope_id,p.model,p.resolution,p.unit_price_usd,p.effective_at,p.note,p.created_by,p.created_at`

func scanProfitPrices(rows *sql.Rows, names bool) ([]service.ImageProcurementPrice, error) {
	prices := []service.ImageProcurementPrice{}
	for rows.Next() {
		var p service.ImageProcurementPrice
		dest := []any{&p.ID, &p.ScopeType, &p.ScopeID, &p.Model, &p.Resolution, &p.UnitPrice, &p.EffectiveAt, &p.Note, &p.CreatedBy, &p.CreatedAt}
		if names {
			dest = append(dest, &p.ScopeName)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		prices = append(prices, p)
	}
	return prices, rows.Err()
}

func (r *imageProfitRepository) ListPrices(ctx context.Context, page, size int) ([]service.ImageProcurementPrice, int, error) {
	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM image_procurement_prices`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+procurementPriceColumns+`,COALESCE(a.name,c.name,'') FROM image_procurement_prices p LEFT JOIN accounts a ON p.scope_type='account' AND a.id=p.scope_id LEFT JOIN channels c ON p.scope_type='channel' AND c.id=p.scope_id ORDER BY p.effective_at DESC,p.id DESC LIMIT $1 OFFSET $2`, size, (page-1)*size)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	prices, err := scanProfitPrices(rows, true)
	return prices, total, err
}

func (r *imageProfitRepository) CreatePrice(ctx context.Context, p *service.ImageProcurementPrice) error {
	var exists bool
	// Soft-deleted accounts may still need historical procurement corrections.
	query := `SELECT EXISTS(SELECT 1 FROM accounts WHERE id=$1)`
	if p.ScopeType == "channel" {
		query = `SELECT EXISTS(SELECT 1 FROM channels WHERE id=$1)`
	}
	if err := r.db.QueryRowContext(ctx, query, p.ScopeID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return appErrors.BadRequest("INVALID_PRICE_SCOPE", "Account or channel does not exist")
	}
	return r.db.QueryRowContext(ctx, `INSERT INTO image_procurement_prices(scope_type,scope_id,model,resolution,unit_price_usd,effective_at,note,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id,created_at`, p.ScopeType, p.ScopeID, p.Model, p.Resolution, p.UnitPrice, p.EffectiveAt, p.Note, p.CreatedBy).Scan(&p.ID, &p.CreatedAt)
}

func decodeProfitReport(id int64, raw []byte) (*service.ImageProfitReport, error) {
	var report service.ImageProfitReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, fmt.Errorf("decode profit report: %w", err)
	}
	report.ID = id
	return &report, nil
}

func (r *imageProfitRepository) ListReports(ctx context.Context, start, end string, history bool) ([]service.ImageProfitReport, error) {
	query := `SELECT id,result-'lines'-'source_ids' FROM (SELECT DISTINCT ON(report_date) id,report_date,result FROM image_profit_reports WHERE report_date BETWEEN $1::date AND $2::date ORDER BY report_date DESC,revision DESC) latest ORDER BY report_date DESC`
	if history {
		query = `SELECT id,result-'lines'-'source_ids' FROM image_profit_reports WHERE report_date BETWEEN $1::date AND $2::date ORDER BY report_date DESC,revision DESC`
	}
	rows, err := r.db.QueryContext(ctx, query, start, end)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	reports := []service.ImageProfitReport{}
	for rows.Next() {
		var id int64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		p, err := decodeProfitReport(id, raw)
		if err != nil {
			return nil, err
		}
		reports = append(reports, *p)
	}
	return reports, rows.Err()
}

func (r *imageProfitRepository) GetReport(ctx context.Context, id int64) (*service.ImageProfitReport, error) {
	var raw []byte
	err := r.db.QueryRowContext(ctx, `SELECT result-'source_ids' FROM image_profit_reports WHERE id=$1`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, appErrors.NotFound("PROFIT_REPORT_NOT_FOUND", "Profit report not found")
	}
	if err != nil {
		return nil, err
	}
	return decodeProfitReport(id, raw)
}

const imageProfitUsagePredicate = `u.billing_type=0 AND COALESCE(u.video_count,0)=0 AND (u.image_count>0 OR u.image_output_tokens>0 OR u.billing_mode='image')`

func (r *imageProfitRepository) PendingDates(ctx context.Context, through time.Time, limit int) ([]time.Time, error) {
	rows, err := r.db.QueryContext(ctx, `WITH first_day AS (
	 SELECT COALESCE((SELECT (u.created_at AT TIME ZONE 'Asia/Shanghai')::date FROM usage_logs u WHERE `+imageProfitUsagePredicate+` ORDER BY u.created_at LIMIT 1),$1::date) AS day
 ) SELECT to_char(d.day,'YYYY-MM-DD') FROM first_day f CROSS JOIN LATERAL generate_series(f.day::timestamp,$1::date::timestamp,interval '1 day') d(day)
	 WHERE NOT EXISTS(SELECT 1 FROM image_profit_reports r WHERE r.report_date=d.day::date)
 ORDER BY d.day LIMIT $2`, through.Format("2006-01-02"), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	dates := []time.Time{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		day, err := time.ParseInLocation("2006-01-02", value, service.ImageProfitLocation)
		if err != nil {
			return nil, err
		}
		dates = append(dates, day)
	}
	return dates, rows.Err()
}

func (r *imageProfitRepository) Settle(ctx context.Context, day time.Time, reason string, actor int64, force bool) (*service.ImageProfitReport, error) {
	date := day.In(service.ImageProfitLocation).Format("2006-01-02")
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// Read committed + date-scoped lock: a waiting worker sees the preceding commit.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('image_profit:' || $1,0))`, date); err != nil {
		return nil, err
	}
	var oldID int64
	var oldRaw []byte
	var old *service.ImageProfitReport
	err = tx.QueryRowContext(ctx, `SELECT id,result FROM image_profit_reports WHERE report_date=$1 ORDER BY revision DESC LIMIT 1`, date).Scan(&oldID, &oldRaw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		old, err = decodeProfitReport(oldID, oldRaw)
		if err != nil {
			return nil, err
		}
		if !force {
			if err = tx.Commit(); err != nil {
				return nil, err
			}
			return old, nil
		}
	}
	pricesRows, err := tx.QueryContext(ctx, `SELECT `+procurementPriceColumns+` FROM image_procurement_prices p WHERE effective_at<$1`, day.AddDate(0, 0, 1))
	if err != nil {
		return nil, err
	}
	prices, err := scanProfitPrices(pricesRows, false)
	_ = pricesRows.Close()
	if err != nil {
		return nil, err
	}
	calculator := service.NewImageProfitCalculator(prices)
	rows, err := tx.QueryContext(ctx, `SELECT u.id,u.account_id,COALESCE(u.channel_id,0),COALESCE(a.name,''),COALESCE(c.name,''),COALESCE(NULLIF(u.upstream_model,''),u.model),COALESCE(u.image_size,''),u.image_count,COALESCE(u.image_size_breakdown,'{}'::jsonb),u.actual_cost,u.created_at FROM usage_logs u LEFT JOIN accounts a ON a.id=u.account_id LEFT JOIN channels c ON c.id=u.channel_id WHERE `+imageProfitUsagePredicate+` AND u.created_at >= $1 AND u.created_at < $2 ORDER BY u.id`, day, day.AddDate(0, 0, 1))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var u service.ImageProfitUsage
		var breakdown []byte
		if err = rows.Scan(&u.ID, &u.AccountID, &u.ChannelID, &u.AccountName, &u.ChannelName, &u.Model, &u.Resolution, &u.ImageCount, &breakdown, &u.Revenue, &u.CreatedAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err = json.Unmarshal(breakdown, &u.Breakdown); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("invalid image size breakdown: %w", err)
		}
		calculator.Add(u)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	report := calculator.Report()
	report.Date = date
	report.Revision = 1
	report.Reason = reason
	report.CreatedBy = actor
	report.CreatedAt = time.Now()
	if old != nil {
		report.Revision = old.Revision + 1
		if report.RequestCount < old.RequestCount {
			return nil, appErrors.Conflict("PROFIT_SOURCE_INCOMPLETE", "Usage records are missing; the previous report was preserved")
		}
		ids := make(map[int64]bool, len(report.SourceIDs))
		for _, id := range report.SourceIDs {
			ids[id] = true
		}
		for _, id := range old.SourceIDs {
			if !ids[id] {
				return nil, appErrors.Conflict("PROFIT_SOURCE_INCOMPLETE", "Usage records are missing; the previous report was preserved")
			}
		}
	}
	var earliest sql.NullTime
	if err = tx.QueryRowContext(ctx, `SELECT MIN(created_at) FROM usage_logs`).Scan(&earliest); err != nil {
		return nil, err
	}
	if !earliest.Valid || day.Before(earliest.Time) {
		report.SourceIncomplete = true
		report.Status = "incomplete"
		report.Profit = nil
		for _, line := range report.Lines {
			line.Profit = nil
		}
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO image_profit_reports(report_date,revision,status,result,reason,created_by,created_at) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`, date, report.Revision, report.Status, string(raw), reason, actor, report.CreatedAt).Scan(&report.ID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &report, nil
}
