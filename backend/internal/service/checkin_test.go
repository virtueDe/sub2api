package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type checkInSettingRepoStub struct {
	values map[string]string
}

func (r *checkInSettingRepoStub) Get(context.Context, string) (*Setting, error) {
	panic("unexpected Get call")
}

func (r *checkInSettingRepoStub) GetValue(context.Context, string) (string, error) {
	panic("unexpected GetValue call")
}

func (r *checkInSettingRepoStub) Set(context.Context, string, string) error {
	panic("unexpected Set call")
}

func (r *checkInSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

func (r *checkInSettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	panic("unexpected SetMultiple call")
}

func (r *checkInSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

func (r *checkInSettingRepoStub) Delete(context.Context, string) error {
	panic("unexpected Delete call")
}

func TestCheckInStatusIncludesConfig(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	repo := &checkInSettingRepoStub{values: checkInConfigValues("request", "3", "0")}
	service := NewCheckInService(db, repo, nil)

	mock.ExpectQuery("SELECT id, username, email, balance FROM users").
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "email", "balance"}).AddRow(7, "demo", "demo@example.com", 12.5))
	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM check_in_records").
		WithArgs(int64(7), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"checked", "today_reward", "total_reward"}).AddRow(false, 0, 0))
	mock.ExpectQuery("SELECT check_in_date::text FROM check_in_records").
		WithArgs(int64(7), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"check_in_date"}))

	status, err := service.GetStatus(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, "request", status.Config.Condition)
	require.Equal(t, float64(3), status.Config.RequestThreshold)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCheckInReturnsSpecificConditionErrors(t *testing.T) {
	tests := []struct {
		name             string
		condition        string
		requestThreshold string
		spendThreshold   string
		requests         int64
		spend            float64
		reason           string
		metadata         map[string]string
	}{
		{
			name:             "request threshold",
			condition:        "request",
			requestThreshold: "3",
			spendThreshold:   "0",
			requests:         1,
			spend:            0,
			reason:           "CHECK_IN_REQUEST_THRESHOLD_NOT_MET",
			metadata:         map[string]string{"current": "1", "required": "3"},
		},
		{
			name:             "consumption threshold",
			condition:        "consumption",
			requestThreshold: "0",
			spendThreshold:   "2.5",
			requests:         4,
			spend:            1.25,
			reason:           "CHECK_IN_CONSUMPTION_THRESHOLD_NOT_MET",
			metadata:         map[string]string{"current": "1.25", "required": "2.5"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() {
				mock.ExpectClose()
				require.NoError(t, db.Close())
				require.NoError(t, mock.ExpectationsWereMet())
			})

			repo := &checkInSettingRepoStub{values: checkInConfigValues(tt.condition, tt.requestThreshold, tt.spendThreshold)}
			service := NewCheckInService(db, repo, nil)

			mock.ExpectBegin()
			mock.ExpectQuery("SELECT role,status,balance FROM users").
				WithArgs(int64(7)).
				WillReturnRows(sqlmock.NewRows([]string{"role", "status", "balance"}).AddRow(RoleUser, StatusActive, 10))
			mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM check_in_records").
				WithArgs(int64(7), sqlmock.AnyArg()).
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
			mock.ExpectQuery("SELECT COUNT\\(\\*\\), COALESCE\\(SUM\\(actual_cost\\),0\\) FROM usage_logs").
				WithArgs(int64(7), sqlmock.AnyArg(), sqlmock.AnyArg()).
				WillReturnRows(sqlmock.NewRows([]string{"requests", "spend"}).AddRow(tt.requests, tt.spend))
			mock.ExpectRollback()

			_, _, err = service.CheckIn(context.Background(), 7)
			require.Error(t, err)
			appErr := infraerrors.FromError(err)
			require.Equal(t, int32(http.StatusBadRequest), appErr.Code)
			require.Equal(t, tt.reason, appErr.Reason)
			require.Equal(t, tt.metadata, appErr.Metadata)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func checkInConfigValues(condition, requestThreshold, spendThreshold string) map[string]string {
	return map[string]string{
		SettingKeyCheckInEnabled:              "true",
		SettingKeyCheckInRewardMin:            "1",
		SettingKeyCheckInRewardMax:            "1",
		SettingKeyCheckInCondition:            condition,
		SettingKeyCheckInRequestThreshold:     requestThreshold,
		SettingKeyCheckInConsumptionThreshold: spendThreshold,
	}
}
