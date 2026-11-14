//go:build linux

package dbstats

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLinuxSystemSampleScopeAndBounds(t *testing.T) {
	s := systemSample()
	require.Contains(t, s["cpu"], "cpu ")
	require.Contains(t, s["process_cpu"], "user_ticks=")
	require.Contains(t, s["process_faults"], "minor_faults=")
	require.Equal(t, "cpu", s["scheduler_source"])
	require.Equal(t, "process_cpu", s["process_faults_source"])
	require.Equal(t, s["cpu_status"], s["scheduler_status"])
	require.Equal(t, s["process_cpu_status"], s["process_faults_status"])
	require.Equal(t, "application_self_only", s["process_scope"])
	require.Equal(t, "unavailable", s["postgres_process_cpu"])
	require.Equal(t, "not_sampled", s["cgroup"])
	for _, name := range []string{"cpu", "memory", "vmstat", "diskstats", "cpu_psi", "memory_psi", "io_psi", "process_cpu", "process_memory", "process_status"} {
		require.LessOrEqual(t, len(s[name]), 16384, name)
		require.Contains(t, []string{"ok", "truncated", "unavailable"}, s[name+"_status"], name)
		started, err := time.Parse(time.RFC3339Nano, s[name+"_started_at"])
		require.NoError(t, err, name)
		finished, err := time.Parse(time.RFC3339Nano, s[name+"_finished_at"])
		require.NoError(t, err, name)
		require.False(t, finished.Before(started), name)
		elapsed, err := strconv.ParseInt(s[name+"_elapsed_us"], 10, 64)
		require.NoError(t, err, name)
		require.GreaterOrEqual(t, elapsed, int64(0), name)
	}
}

func TestLinuxKernelCounterSelection(t *testing.T) {
	for _, tc := range []struct {
		name, source, raw, want string
		truncated               bool
	}{
		{
			name: "scheduler excludes per-CPU and interrupt totals", source: "scheduler",
			raw:  "cpu 1 2 3 4\ncpu0 1 2 3 4\nintr 100\nctxt 42\nprocesses 8\nprocs_running 2\nprocs_blocked 1\nsoftirq 90 10\n",
			want: "ctxt 42\nprocesses 8\nprocs_running 2\nprocs_blocked 1",
		},
		{
			name: "scheduler truncated before tail", source: "scheduler", raw: "cpu 1 2 3\ncpu0 1 2 3\n", truncated: true,
		},
		{
			name: "truncated number omitted", source: "vmstat", raw: "pgfault 42\npgmajfault 123", truncated: true, want: "pgfault 42",
		},
		{
			name: "truncated first line omitted", source: "vmstat", raw: "pgfault 123", truncated: true,
		},
		{
			name: "missing counters stay absent", source: "vmstat", raw: "nr_free_pages 100\npgfault 9\n", want: "pgfault 9",
		},
		{
			name: "zone and legacy counters retain original names", source: "vmstat",
			raw:  "allocstall 2\nallocstall_normal 3\nallocstall_movable 4\npgscan_direct 5\npgsteal_kswapd 6\n",
			want: "allocstall 2\nallocstall_normal 3\nallocstall_movable 4\npgscan_direct 5\npgsteal_kswapd 6",
		},
		{
			name: "compaction paging and hugepage counters", source: "vmstat",
			raw:  "compact_stall 1\ncompact_daemon_migrate_scanned 2\nthp_fault_fallback 3\npswpin 4\npswpout 5\npgmajfault 6\noom_kill 0\n",
			want: "compact_stall 1\ncompact_daemon_migrate_scanned 2\nthp_fault_fallback 3\npswpin 4\npswpout 5\npgmajfault 6\noom_kill 0",
		},
		{
			name: "full uint64 precision and invalid numbers", source: "vmstat",
			raw:  "pgfault 18446744073709551615\npgmajfault -1\npswpin 18446744073709551616\npswpout invalid\ncompact_stall 1 2\n",
			want: "pgfault 18446744073709551615",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, selectKernelCounters(tc.raw, tc.source, tc.truncated))
		})
	}
}

func TestLinuxProcessCounters(t *testing.T) {
	// Fields begin at kernel stat field 3 (state); child faults differ on purpose.
	fields := []string{"S", "1", "2", "3", "4", "5", "6", "123", "999", "45", "888", "67", "89"}
	raw := "42 (worker name ) with parentheses) " + strings.Join(fields, " ") + "\n"
	cpu, faults := processCounters(raw)
	require.Equal(t, "user_ticks=67 system_ticks=89", cpu)
	require.Equal(t, "minor_faults=123 major_faults=45", faults)
	for _, invalid := range []string{"", "42 missing-parentheses", "42 () S 1", "42 (worker) " + strings.Join(append(fields[:12:12], "invalid"), " ")} {
		cpu, faults = processCounters(invalid)
		require.Equal(t, "unavailable", cpu)
		require.Equal(t, "unavailable", faults)
	}
}
