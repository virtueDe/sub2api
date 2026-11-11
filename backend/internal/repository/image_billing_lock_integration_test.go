//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestImageReservationParentLocksAllowForeignKeyChecks(t *testing.T) {
	for _, subscription := range []bool{false, true} {
		t.Run(fmt.Sprint(subscription), func(t *testing.T) {
			r, snapshot, ref := imageReservationFixture(t)
			var subID *int64
			if subscription {
				client := testEntClient(t)
				group := mustCreateGroup(t, client, &service.Group{Name: uuid.NewString(), Platform: service.PlatformGemini, SubscriptionType: service.SubscriptionTypeSubscription})
				sub := mustCreateSubscription(t, client, &service.UserSubscription{UserID: snapshot.Command.UserID, GroupID: group.ID})
				subID = &sub.ID
				_, err := integrationDB.Exec(`UPDATE api_keys SET group_id=$2 WHERE id=$1`, ref.APIKeyID, group.ID)
				require.NoError(t, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			holder, err := integrationDB.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = holder.Rollback() }()
			// Foreign-key checks in a concurrent usage INSERT hold these modes
			// until commit. Reservation must complete while those locks remain.
			_, err = holder.ExecContext(ctx, `SELECT id FROM users WHERE id=$1 FOR KEY SHARE`, snapshot.Command.UserID)
			require.NoError(t, err)
			_, err = holder.ExecContext(ctx, `SELECT id FROM api_keys WHERE id=$1 FOR KEY SHARE`, ref.APIKeyID)
			require.NoError(t, err)
			if subID != nil {
				_, err = holder.ExecContext(ctx, `SELECT id FROM user_subscriptions WHERE id=$1 FOR KEY SHARE`, *subID)
				require.NoError(t, err)
			}
			require.NoError(t, r.ReserveImageSettlement(ctx, ref, snapshot.Command.UserID, subID, 2))
			if subID == nil {
				checkImageWallet(t, snapshot.Command.UserID, 98, 2)
			} else {
				checkImageWallet(t, snapshot.Command.UserID, 100, 0)
				tx, err := integrationDB.BeginTx(ctx, nil)
				require.NoError(t, err)
				defer func() { _ = tx.Rollback() }()
				require.NoError(t, incrementSettlementSubscription(ctx, tx, *subID, 0.75, time.Now()))
				require.NoError(t, tx.Commit())
			}
		})
	}
}

func TestImageReservationParentLocksStillSerializeWriters(t *testing.T) {
	for _, table := range []string{"users", "api_keys", "user_subscriptions"} {
		t.Run(table, func(t *testing.T) {
			r, snapshot, ref := imageReservationFixture(t)
			id := snapshot.Command.UserID
			var subID *int64
			if table == "api_keys" {
				id = ref.APIKeyID
			} else if table == "user_subscriptions" {
				client := testEntClient(t)
				group := mustCreateGroup(t, client, &service.Group{Name: uuid.NewString(), Platform: service.PlatformGemini, SubscriptionType: service.SubscriptionTypeSubscription})
				sub := mustCreateSubscription(t, client, &service.UserSubscription{UserID: id, GroupID: group.ID})
				id, subID = sub.ID, &sub.ID
				_, err := integrationDB.Exec(`UPDATE api_keys SET group_id=$2 WHERE id=$1`, ref.APIKeyID, group.ID)
				require.NoError(t, err)
			}
			holder, err := integrationDB.BeginTx(context.Background(), nil)
			require.NoError(t, err)
			defer func() { _ = holder.Rollback() }()
			_, err = holder.Exec(`SELECT id FROM `+table+` WHERE id=$1 FOR NO KEY UPDATE`, id)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			err = r.ReserveImageSettlement(ctx, ref, snapshot.Command.UserID, subID, 2)
			require.Error(t, err, "conflicting writers must remain serialized")
			require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
			require.NoError(t, holder.Rollback())
			checkImageWallet(t, snapshot.Command.UserID, 100, 0)
			require.NoError(t, r.ReserveImageSettlement(context.Background(), ref, snapshot.Command.UserID, subID, 2))
		})
	}
}
