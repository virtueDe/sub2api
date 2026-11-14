package dbstats

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestDiagnosticsBaselineAndNonblockingTrigger(t *testing.T) {
	current := sql.DBStats{MaxOpenConnections: 48, WaitCount: 100, WaitDuration: time.Second}
	r := &Recorder{pools: map[string]func() sql.DBStats{"core": func() sql.DBStats { return current }}, previous: map[string]sql.DBStats{"core": current}, triggers: make(chan diagnosticTrigger, 8)}
	sample := r.sample(time.Now())
	require.Zero(t, sample.Pools["core"].WaitCountDelta)
	current.WaitCount += 3
	current.WaitDuration += 4 * time.Millisecond
	sample = r.sample(time.Now())
	require.Equal(t, int64(3), sample.Pools["core"].WaitCountDelta)
	require.Equal(t, int64(4000), sample.Pools["core"].WaitMicrosecondsDelta)
	for i := 0; i < 1000; i++ {
		r.Trigger("billing_error")
	}
	require.Len(t, r.triggers, 8)
	require.Equal(t, uint64(992), r.droppedTriggers.Load())
	r.Trigger("request-secret")
	require.Len(t, r.triggers, 8)
}
func TestDiagnosticsBoundedFileRetention(t *testing.T) {
	r := &Recorder{dir: t.TempDir()}
	for i := 0; i < 20; i++ {
		require.NoError(t, r.writeIncident(incident{Reason: "billing_error", TriggeredAt: time.Unix(int64(i*60), 0)}))
	}
	entries, err := os.ReadDir(r.dir)
	require.NoError(t, err)
	require.Len(t, entries, 8)
	for _, entry := range entries {
		require.Contains(t, entry.Name(), "incident-")
		require.NotContains(t, entry.Name(), "tmp")
	}
	require.NoError(t, r.writeIncident(incident{Samples: []DiagnosticSample{{System: map[string]string{"oversize": strings.Repeat("x", 3<<20)}}}}))
	data, err := os.ReadFile(filepath.Join(r.dir, "incident-0.json"))
	require.NoError(t, err)
	var saved incident
	require.NoError(t, json.Unmarshal(data, &saved))
	require.True(t, saved.Truncated)
	require.Equal(t, 1, saved.OmittedSamples)
}

func TestDiagnosticsShutdownPreservesTriggeredEvidence(t *testing.T) {
	dir := t.TempDir()
	r, err := NewRecorder(dir, map[string]func() sql.DBStats{"core": func() sql.DBStats { return sql.DBStats{MaxOpenConnections: 48, InUse: 48} }}, nil)
	require.NoError(t, err)
	r.Trigger("billing_error")
	r.Stop()
	r.Stop()
	files, err := filepath.Glob(filepath.Join(dir, "incident-*.json"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	data, err := os.ReadFile(files[0])
	require.NoError(t, err)
	var saved incident
	require.NoError(t, json.Unmarshal(data, &saved))
	require.Equal(t, "billing_error", saved.Reason)
	require.NotEmpty(t, saved.Samples)
	require.Equal(t, 48, saved.Samples[len(saved.Samples)-1].Pools["core"].InUse)
}

func TestDiagnosticsTruncationRetainsNewestSamples(t *testing.T) {
	r := &Recorder{dir: t.TempDir()}
	samples := make([]DiagnosticSample, 80)
	for i := range samples {
		samples[i] = DiagnosticSample{At: time.Unix(int64(i), 0), System: map[string]string{"diskstats": strings.Repeat("x", 16384), "memory": strings.Repeat("y", 16384)}}
	}
	require.NoError(t, r.writeIncident(incident{TriggeredAt: time.Unix(0, 0), Samples: samples}))
	data, err := os.ReadFile(filepath.Join(r.dir, "incident-0.json"))
	require.NoError(t, err)
	require.LessOrEqual(t, len(data), 2<<20)
	var saved incident
	require.NoError(t, json.Unmarshal(data, &saved))
	require.True(t, saved.Truncated)
	require.Equal(t, 80, len(saved.Samples)+saved.OmittedSamples)
	require.True(t, samples[79].At.Equal(saved.Samples[len(saved.Samples)-1].At))
}
func TestDiagnosticsTimeoutClassification(t *testing.T) {
	require.True(t, IsTimeout(context.DeadlineExceeded))
	require.False(t, IsTimeout(context.Canceled))
	require.True(t, IsTimeout(&pq.Error{Code: "57014", Message: "canceling statement due to statement timeout"}))
}

func TestDiagnosticsUsesActualCollectionTimes(t *testing.T) {
	scheduled := time.Now().Add(-5 * time.Second)
	r := &Recorder{
		pools: map[string]func() sql.DBStats{"core": func() sql.DBStats {
			time.Sleep(20 * time.Millisecond)
			return sql.DBStats{}
		}},
		previous: make(map[string]sql.DBStats),
	}
	before := time.Now()
	sample := r.sample(scheduled)
	require.Equal(t, scheduled.UTC(), sample.ScheduledAt)
	require.False(t, sample.At.Before(before))
	require.True(t, sample.FinishedAt.After(sample.At))
	require.GreaterOrEqual(t, sample.SchedulingDelayMicroseconds, int64(5*time.Second/time.Microsecond))
	require.GreaterOrEqual(t, sample.ElapsedMicroseconds, int64(20*time.Millisecond/time.Microsecond))
}

func TestDiagnosticsProbeRecordsDeadlineOverrun(t *testing.T) {
	completed := make(chan struct{})
	r, err := NewRecorder(t.TempDir(), nil, func(ctx context.Context) DatabaseProbe {
		<-ctx.Done()
		// A driver may finish after cancellation; keep the real elapsed time.
		time.Sleep(20 * time.Millisecond)
		select {
		case <-completed:
		default:
			close(completed)
		}
		return DatabaseProbe{At: time.Unix(0, 0), Status: "timeout"}
	})
	require.NoError(t, err)
	t.Cleanup(r.Stop)
	select {
	case <-completed:
	case <-time.After(6 * time.Second):
		t.Fatal("probe did not finish")
	}
	r.Stop()
	probe := r.lastProbe
	require.Equal(t, "timeout", probe.Status)
	require.True(t, probe.At.After(time.Unix(0, 0)))
	require.True(t, probe.FinishedAt.After(probe.At))
	require.GreaterOrEqual(t, probe.ElapsedMicroseconds, int64(750*time.Millisecond/time.Microsecond))
}
