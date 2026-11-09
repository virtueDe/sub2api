//go:build linux

package dbstats

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLinuxSystemSampleScopeAndBounds(t *testing.T) {
	s := systemSample()
	require.Contains(t, s["cpu"], "cpu ")
	require.Contains(t, s["process_cpu"], "user_ticks=")
	require.Equal(t, "application_self_only", s["process_scope"])
	require.Equal(t, "unavailable", s["postgres_process_cpu"])
	require.Equal(t, "not_sampled", s["cgroup"])
	for _, name := range []string{"cpu", "memory", "diskstats", "cpu_psi", "memory_psi", "io_psi", "process_cpu", "process_memory", "process_status"} {
		require.LessOrEqual(t, len(s[name]), 16384, name)
		require.Contains(t, []string{"ok", "truncated", "unavailable"}, s[name+"_status"], name)
	}
}
