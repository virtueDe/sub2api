package repository

import (
	"testing"
	"time"

	appTimezone "github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

func useGroupUsageRepositoryTestTimezone(t *testing.T, name string) {
	t.Helper()

	previousName := appTimezone.Name()
	previousLocal := time.Local
	require.NoError(t, appTimezone.Init(name))
	t.Cleanup(func() {
		time.Local = previousLocal
		require.NoError(t, appTimezone.Init(previousName))
	})
}
