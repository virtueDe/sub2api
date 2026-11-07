//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGetErrorLogByID_APIKeyPrefixAndUpstreamStatus(t *testing.T) {
	ctx := context.Background()
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE ops_error_logs RESTART IDENTITY CASCADE")
	repo := NewOpsRepository(integrationDB).(*opsRepository)

	var plainID int64
	err := integrationDB.QueryRowContext(ctx, `
		INSERT INTO ops_error_logs (
			error_phase, error_type, severity, status_code, created_at
		) VALUES (
			'upstream', 'upstream_error', 'error', 500, NOW()
		) RETURNING id`,
	).Scan(&plainID)
	require.NoError(t, err)

	plain, err := repo.GetErrorLogByID(ctx, plainID)
	require.NoError(t, err)
	require.Empty(t, plain.APIKeyPrefix)

	validID, err := repo.InsertErrorLog(ctx, &service.OpsInsertErrorLogInput{
		ErrorPhase:   "request",
		ErrorType:    "api_error",
		Severity:     "error",
		StatusCode:   402,
		CreatedAt:    time.Now(),
		APIKeyPrefix: "sk-valid",
	})
	require.NoError(t, err)

	valid, err := repo.GetErrorLogByID(ctx, validID)
	require.NoError(t, err)
	require.Equal(t, "sk-valid", valid.APIKeyPrefix)

	zero := 0
	credentialFailureID, err := repo.InsertErrorLog(ctx, &service.OpsInsertErrorLogInput{
		ErrorPhase:         "account_auth",
		ErrorType:          "upstream_error",
		Severity:           "error",
		StatusCode:         503,
		UpstreamStatusCode: &zero,
		CreatedAt:          time.Now(),
	})
	require.NoError(t, err)

	credentialFailure, err := repo.GetErrorLogByID(ctx, credentialFailureID)
	require.NoError(t, err)
	require.NotNil(t, credentialFailure.UpstreamStatusCode)
	require.Zero(t, *credentialFailure.UpstreamStatusCode)
}

func TestOpsErrorLogs_ResponseStatusAndUpstreamFilters(t *testing.T) {
	ctx := context.Background()
	repo := NewOpsRepository(integrationDB).(*opsRepository)
	ptr := func(code int) *int { return &code }
	cases := []struct {
		name     string
		upstream *int
		client   int
		filter   int
	}{
		{"mapped", ptr(400), 451, 400},
		{"unchanged", ptr(400), 400, 400},
		{"upstream451", ptr(451), 451, 451},
		{"no_upstream", nil, 451, 451},
		{"zero_upstream", ptr(0), 503, 0},
		{"recovered", ptr(503), 200, 503},
	}
	prefix := t.Name() + ":"
	ids := make(map[string]int64)
	for _, tc := range cases {
		id, err := repo.InsertErrorLog(ctx, &service.OpsInsertErrorLogInput{
			RequestID: prefix + tc.name, ErrorPhase: "upstream", ErrorType: "upstream_error",
			Severity: "error", StatusCode: tc.client, UpstreamStatusCode: tc.upstream,
			CreatedAt: time.Now(),
		})
		require.NoError(t, err)
		ids[tc.name] = id
		detail, err := repo.GetErrorLogByID(ctx, id)
		require.NoError(t, err)
		require.Equal(t, tc.filter, detail.StatusCode, tc.name)
		require.Equal(t, tc.upstream, detail.UpstreamStatusCode, tc.name)
		require.Equal(t, ptr(tc.client), detail.ClientStatusCode, tc.name)
	}
	filter := &service.OpsErrorLogFilter{
		View: "all", Phase: "upstream", IncludeRecoveredUpstream: true,
		Query: prefix, SortBy: "status_code", SortOrder: "asc",
	}
	list, err := repo.ListErrorLogs(ctx, filter)
	require.NoError(t, err)
	require.Equal(t, len(cases), list.Total)
	byID := make(map[int64]*service.OpsErrorLog)
	var sortedStatuses []int
	for _, row := range list.Errors {
		byID[row.ID] = row
		sortedStatuses = append(sortedStatuses, row.StatusCode)
	}
	require.Equal(t, []int{0, 400, 400, 451, 451, 503}, sortedStatuses)
	for _, tc := range cases {
		row := byID[ids[tc.name]]
		require.NotNil(t, row, tc.name)
		require.Equal(t, tc.upstream, row.UpstreamStatusCode, tc.name)
		require.Equal(t, ptr(tc.client), row.ClientStatusCode, tc.name)
	}
	for _, tc := range []struct {
		code  int
		names []string
	}{
		{400, []string{"mapped", "unchanged"}},
		{451, []string{"upstream451", "no_upstream"}},
	} {
		filter.StatusCodes = []int{tc.code}
		list, err := repo.ListErrorLogs(ctx, filter)
		require.NoError(t, err)
		var gotIDs, wantIDs []int64
		for _, row := range list.Errors {
			gotIDs = append(gotIDs, row.ID)
		}
		for _, name := range tc.names {
			wantIDs = append(wantIDs, ids[name])
		}
		require.ElementsMatch(t, wantIDs, gotIDs, "filter by upstream code %d", tc.code)
	}
	filter.StatusCodes = nil
	filter.StatusCodesOther = true
	list, err = repo.ListErrorLogs(ctx, filter)
	require.NoError(t, err)
	require.Len(t, list.Errors, 1)
	require.Equal(t, ids["zero_upstream"], list.Errors[0].ID)
}
