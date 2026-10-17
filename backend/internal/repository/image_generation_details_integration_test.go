//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestImageGenerationUsageDetailPersistence(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newUsageLogRepositoryWithSQL(client, integrationDB)
	user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("image-detail-%s@example.com", uuid.NewString())})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-image-detail-" + uuid.NewString(), Name: "images"})
	account := mustCreateAccount(t, client, &service.Account{Name: "image-detail-" + uuid.NewString()})
	for _, path := range []string{"single", "batch", "best effort"} {
		t.Run(path, func(t *testing.T) {
			detail := &service.ImageGenerationDetail{Prompt: "画一只猫", RequestParams: map[string]any{"size": "1024x1024", "n": float64(1)}, Images: []service.GeneratedImage{{B64JSON: "aW1hZ2U=", MimeType: "image/png"}}}
			usage := &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, RequestID: uuid.NewString(), Model: "gpt-image-2", ImageCount: 1, ImageGeneration: detail}
			switch path {
			case "single":
				_, err := repo.createSingle(ctx, integrationDB, usage)
				require.NoError(t, err)
			case "batch":
				inserted, err := repo.Create(ctx, usage)
				require.NoError(t, err)
				require.True(t, inserted)
			case "best effort":
				require.NoError(t, repo.CreateBestEffort(ctx, usage))
				require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT id FROM usage_logs WHERE request_id = $1 AND api_key_id = $2", usage.RequestID, key.ID).Scan(&usage.ID))
			}
			saved, err := repo.GetByID(ctx, usage.ID)
			require.NoError(t, err)
			require.True(t, saved.HasImageGeneration)
			require.Equal(t, detail, saved.ImageGeneration)
			// Retried billing must not replace the original request or images.
			usage.ImageGeneration = &service.ImageGenerationDetail{Prompt: "retry"}
			inserted, err := repo.Create(ctx, usage)
			require.NoError(t, err)
			require.False(t, inserted)
			saved, err = repo.GetByID(ctx, usage.ID)
			require.NoError(t, err)
			require.Equal(t, detail, saved.ImageGeneration)
			list, _, err := repo.ListByUser(ctx, user.ID, pagination.PaginationParams{Page: 1, PageSize: 20})
			require.NoError(t, err)
			for _, item := range list {
				require.True(t, item.HasImageGeneration)
				require.Nil(t, item.ImageGeneration, "large image payloads must stay out of list responses")
			}
			require.NoError(t, repo.Delete(ctx, usage.ID))
			_, err = repo.GetByID(ctx, usage.ID)
			require.ErrorIs(t, err, service.ErrUsageLogNotFound)
		})
	}
}

func TestImageGenerationErrorDetailPersistence(t *testing.T) {
	ctx := context.Background()
	repo := NewOpsRepository(integrationDB)
	detail := &service.ImageGenerationDetail{Prompt: "cat", RequestParams: map[string]any{"n": float64(2)}, Images: []service.GeneratedImage{{B64JSON: "cGFydGlhbA==", MimeType: "image/png", Partial: true}}}
	id, err := repo.InsertErrorLog(ctx, &service.OpsInsertErrorLogInput{RequestID: uuid.NewString(), ErrorPhase: "upstream", ErrorType: "api_error", Severity: "error", StatusCode: 502, CreatedAt: time.Now(), ImageGeneration: detail})
	require.NoError(t, err)
	saved, err := repo.GetErrorLogByID(ctx, id)
	require.NoError(t, err)
	require.Equal(t, detail, saved.ImageGeneration)
}

func TestImageRequestTypePersistenceAndStatistics(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newUsageLogRepositoryWithSQL(client, integrationDB)
	user := mustCreateUser(t, client, &service.User{Email: "image-types-" + uuid.NewString() + "@example.com"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-image-types-" + uuid.NewString(), Name: "images"})
	account := mustCreateAccount(t, client, &service.Account{Name: "image-types-" + uuid.NewString()})
	start := time.Now().UTC().Truncate(time.Minute)
	end := start.Add(time.Minute)
	imageSize := "1K"
	for _, kind := range []service.RequestType{service.RequestTypeSync, service.RequestTypeImageSync, service.RequestTypeImageAsync} {
		usage := &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, RequestID: uuid.NewString(), Model: "gpt-image-2", RequestType: kind, ImageCount: 1, ImageSize: &imageSize, ActualCost: 0.1, CreatedAt: start.Add(time.Second)}
		_, err := repo.createSingle(ctx, integrationDB, usage)
		require.NoError(t, err)
		saved, err := repo.GetByID(ctx, usage.ID)
		require.NoError(t, err)
		require.Equal(t, kind, saved.RequestType)
		raw := int16(kind)
		filters := UsageLogFilters{UserID: user.ID, RequestType: &raw, ExactTotal: true}
		items, page, err := repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 20}, filters)
		require.NoError(t, err)
		require.EqualValues(t, 1, page.Total)
		require.Len(t, items, 1)
		require.Equal(t, kind, items[0].RequestType)
		stats, err := repo.GetStatsWithFilters(ctx, filters)
		require.NoError(t, err)
		require.EqualValues(t, 1, stats.TotalRequests)
	}
	// The new synchronous image value must still count as a successful request.
	_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(channelMonitorV2UserMetricsSQL, "'openai'", "'gpt-image-2'"), start, end)
	require.NoError(t, err)
	var successes int64
	err = integrationDB.QueryRowContext(ctx, "SELECT SUM(success_requests) FROM channel_monitor_v2_user_metrics_1m WHERE user_id = $1 AND bucket_start = $2", user.ID, start).Scan(&successes)
	require.NoError(t, err)
	require.EqualValues(t, 3, successes)
	_, err = integrationDB.ExecContext(ctx, "UPDATE usage_logs SET request_type = 8 WHERE user_id = $1", user.ID)
	require.Error(t, err, "unrecognized request types must remain constrained")
}
