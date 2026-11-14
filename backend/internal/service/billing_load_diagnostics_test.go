package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/dbstats"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/singleflight"
)

func TestBillingLoadOnlyDependencyFailureTriggersDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name     string
		cause    error
		incident bool
	}{
		{"database_failure", errors.New("database unavailable"), true},
		{"missing_user", ErrUserNotFound, false},
		{"missing_subscription", ErrSubscriptionNotFound, false},
		{"success", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			recorder, err := dbstats.NewRecorder(dir, nil, nil)
			require.NoError(t, err)
			t.Cleanup(recorder.Stop)
			svc := &BillingCacheService{}
			_, err = svc.loadBillingData(context.Background(), &singleflight.Group{}, "diagnostic", func(context.Context) (any, error) {
				return nil, test.cause
			})
			require.Equal(t, test.cause, err)
			recorder.Stop()
			files, err := filepath.Glob(filepath.Join(dir, "incident-*.json"))
			require.NoError(t, err)
			if !test.incident {
				require.Empty(t, files)
				return
			}
			require.Len(t, files, 1)
			data, err := os.ReadFile(files[0])
			require.NoError(t, err)
			var saved struct {
				Reason  string
				Samples []dbstats.DiagnosticSample
			}
			require.NoError(t, json.Unmarshal(data, &saved))
			require.Equal(t, "billing_error", saved.Reason)
			require.NotEmpty(t, saved.Samples)
			require.Positive(t, saved.Samples[len(saved.Samples)-1].Metrics["billing_load"].Failed)
		})
	}
}
