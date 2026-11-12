//go:build integration

package repository

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImageReservationRecoversOversizedSizeMetadata(t *testing.T) {
	ctx := context.Background()
	r, snapshot, ref := imageReservationFixture(t)
	input := strings.Repeat("historical description ", 9)
	output := strings.Repeat("invalid output metadata ", 4)
	snapshot.Usage.ImageInputSize = &input
	snapshot.Usage.ImageOutputSize = &output
	require.NoError(t, r.ReserveImageSettlement(ctx, ref, snapshot.Command.UserID, nil, 2))
	require.NoError(t, r.MarkImageSettlementDispatched(ctx, ref))
	require.NoError(t, r.PrepareSettlement(ctx, snapshot))
	checkImageWallet(t, snapshot.Command.UserID, 98, 2)
	var originalFacts string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT snapshot::text FROM usage_settlements WHERE request_id=$1 AND api_key_id=$2`, ref.RequestID, ref.APIKeyID).Scan(&originalFacts))
	// Reconstruct the repository as a restarted worker would. It must use the
	// original immutable price/fingerprint even when audit metadata is invalid.
	replay := &usageBillingRepository{db: integrationDB}
	for attempt := 0; attempt < 3; attempt++ {
		saved, result, err := replay.ApplySettlement(ctx, ref, nil)
		require.NoError(t, err)
		require.Equal(t, attempt == 0, result.Applied)
		require.Equal(t, snapshot.Command, saved.Command)
		require.Equal(t, input, *saved.Usage.ImageInputSize)
		require.Equal(t, output, *saved.Usage.ImageOutputSize)
		checkImageWallet(t, snapshot.Command.UserID, 98.75, 0)
		require.NoError(t, replay.CompleteSettlement(ctx, ref))
	}
	var storedInput, storedOutput sql.NullString
	var actual float64
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT image_input_size,image_output_size,actual_cost FROM usage_logs WHERE request_id=$1 AND api_key_id=$2`, ref.RequestID, ref.APIKeyID).Scan(&storedInput, &storedOutput, &actual))
	require.False(t, storedInput.Valid)
	require.False(t, storedOutput.Valid)
	require.Equal(t, snapshot.Command.BalanceCost, actual)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM usage_logs WHERE request_id=$1 AND api_key_id=$2`, ref.RequestID, ref.APIKeyID).Scan(&count))
	require.Equal(t, 1, count)
	var currentFacts, state string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT snapshot::text,state FROM usage_settlements WHERE request_id=$1 AND api_key_id=$2`, ref.RequestID, ref.APIKeyID).Scan(&currentFacts, &state))
	require.Equal(t, originalFacts, currentFacts)
	require.Equal(t, "complete", state)
}
