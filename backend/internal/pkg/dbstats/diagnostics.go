package dbstats

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/lib/pq"
	"go.uber.org/zap"
)

var metricNames = [...]string{"image_reserve", "image_release", "billing_load", "payer_admission", "billing", "connection_acquire", "billing_begin", "dedup_insert", "dedup_existing_lookup", "dedup_archive_lookup", "subscription", "balance", "api_key_quota", "api_key_rate_limit", "account_quota", "platform_quota", "commit", "settlement", "settlement_begin", "settlement_prepare", "usage_log_single", "usage_log_batch", "usage_log_fallback", "ops_error_batch", "system_log_batch"}
var durationBounds = [...]int64{1000, 5000, 10000, 50000, 100000, 500000, 1000000, 5000000, 15000000}

type diagnosticCounter struct {
	count, failed, timeout, micros atomic.Uint64
	buckets                        [10]atomic.Uint64
}

var counters [len(metricNames)]diagnosticCounter
var activeRecorder atomic.Pointer[Recorder]

// Record uses fixed labels and atomics. It never performs SQL or file I/O on
// the caller, and does not retain error text, request IDs or request payloads.
func Record(stage string, elapsed time.Duration, err error) {
	for i, name := range metricNames {
		if name != stage {
			continue
		}
		c := &counters[i]
		c.count.Add(1)
		us := max(elapsed.Microseconds(), 0)
		c.micros.Add(uint64(us))
		bucket := len(durationBounds)
		for j, upper := range durationBounds {
			if us <= upper {
				bucket = j
				break
			}
		}
		c.buckets[bucket].Add(1)
		if err != nil {
			c.failed.Add(1)
			if IsTimeout(err) {
				c.timeout.Add(1)
			}
		}
		return
	}
}
func IsTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var pg *pq.Error
	return errors.As(err, &pg) && pg.Code == "57014" && pg.Message == "canceling statement due to statement timeout"
}
func TriggerBillingFailure() {
	if r := activeRecorder.Load(); r != nil {
		r.Trigger("billing_error")
	}
}

type MetricSample struct {
	Count, Failed, Timeout, Microseconds uint64
	DurationBuckets                      [10]uint64
}
type PoolSample struct {
	Open, InUse, Idle, MaxOpen int
	WaitCountDelta             int64
	WaitMicrosecondsDelta      int64
}
type DatabaseWait struct {
	PID                                  int
	State, Category, WaitType, WaitEvent string
	AgeSeconds                           float64
	Blockers                             []int64
	BlockerCount                         int
	BlockersTruncated                    bool
}
type DatabaseProbe struct {
	At                  time.Time
	FinishedAt          time.Time
	ElapsedMicroseconds int64
	Status              string
	Visibility          string
	Waits               []DatabaseWait
	Truncated           bool
}
type DiagnosticSample struct {
	At                                                      time.Time
	ScheduledAt, FinishedAt                                 time.Time
	ElapsedMicroseconds, SchedulingDelayMicroseconds        int64
	Pools                                                   map[string]PoolSample
	Metrics                                                 map[string]MetricSample
	StatisticsActive, StatisticsCapacity                    int
	StatisticsStarted, StatisticsRejected, StatisticsFailed uint64
	System                                                  map[string]string
	Database                                                DatabaseProbe
	DroppedTriggers, DroppedFiles, FileFailures             uint64
}
type incident struct {
	Reason                          string
	TriggeredAt                     time.Time
	Samples                         []DiagnosticSample
	DurationBucketUpperMicroseconds []int64
	Truncated                       bool
	OmittedSamples                  int
}
type diagnosticTrigger struct {
	reason string
	at     time.Time
}

type Recorder struct {
	dir                                         string
	pools                                       map[string]func() sql.DBStats
	previous                                    map[string]sql.DBStats
	probe                                       func(context.Context) DatabaseProbe
	probeMu                                     sync.Mutex
	lastProbe                                   DatabaseProbe
	triggers                                    chan diagnosticTrigger
	files                                       chan incident
	cancel                                      context.CancelFunc
	done, writerDone, probeDone                 chan struct{}
	once                                        sync.Once
	droppedTriggers, droppedFiles, fileFailures atomic.Uint64
}

func NewRecorder(dir string, pools map[string]func() sql.DBStats, probe func(context.Context) DatabaseProbe) (*Recorder, error) {
	if dir == "" {
		return nil, errors.New("database diagnostics directory is empty")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("database diagnostics directory must be a real directory")
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &Recorder{dir: dir, pools: pools, previous: make(map[string]sql.DBStats), probe: probe, triggers: make(chan diagnosticTrigger, 8), files: make(chan incident, 1), cancel: cancel, done: make(chan struct{}), writerDone: make(chan struct{}), probeDone: make(chan struct{}), lastProbe: DatabaseProbe{Status: "not_sampled", Visibility: "unavailable"}}
	// Baseline before starting: prior cumulative pool waits are not new pressure.
	for name, stats := range pools {
		r.previous[name] = stats()
	}
	activeRecorder.Store(r)
	go r.collect(ctx)
	go r.writeLoop()
	go r.probeLoop(ctx)
	return r, nil
}
func (r *Recorder) Trigger(reason string) {
	if r == nil {
		return
	}
	if reason != "billing_error" && reason != "pool_wait" {
		return
	}
	select {
	case r.triggers <- diagnosticTrigger{reason, time.Now().UTC()}:
	default:
		r.droppedTriggers.Add(1)
	}
}
func (r *Recorder) sample(scheduled time.Time) DiagnosticSample {
	started := time.Now()
	s := DiagnosticSample{At: started.UTC(), ScheduledAt: scheduled.UTC(), SchedulingDelayMicroseconds: max(started.Sub(scheduled).Microseconds(), 0), Pools: make(map[string]PoolSample, len(r.pools)), Metrics: make(map[string]MetricSample), System: systemSample(), DroppedTriggers: r.droppedTriggers.Load(), DroppedFiles: r.droppedFiles.Load(), FileFailures: r.fileFailures.Load()}
	for name, stats := range r.pools {
		current := stats()
		prev := r.previous[name]
		r.previous[name] = current
		s.Pools[name] = PoolSample{current.OpenConnections, current.InUse, current.Idle, current.MaxOpenConnections, max(current.WaitCount-prev.WaitCount, 0), max((current.WaitDuration - prev.WaitDuration).Microseconds(), 0)}
	}
	for i, name := range metricNames {
		c := &counters[i]
		if c.count.Load() == 0 {
			continue
		}
		m := MetricSample{Count: c.count.Load(), Failed: c.failed.Load(), Timeout: c.timeout.Load(), Microseconds: c.micros.Load()}
		for j := range m.DurationBuckets {
			m.DurationBuckets[j] = c.buckets[j].Load()
		}
		s.Metrics[name] = m
	}
	b := Default
	s.StatisticsActive = len(b.slots)
	s.StatisticsCapacity = cap(b.slots)
	s.StatisticsStarted = b.started.Load()
	s.StatisticsRejected = b.rejected.Load()
	s.StatisticsFailed = b.failed.Load()
	r.probeMu.Lock()
	s.Database = r.lastProbe
	r.probeMu.Unlock()
	s.FinishedAt = time.Now().UTC()
	s.ElapsedMicroseconds = time.Since(started).Microseconds()
	return s
}
func (r *Recorder) collect(ctx context.Context) {
	defer close(r.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	ring := make([]DiagnosticSample, 80)
	next, count := 0, 0
	var pending *diagnosticTrigger
	var lastIncident time.Time
	waitStreak := 0
	flush := func() {
		if pending == nil {
			return
		}
		samples := make([]DiagnosticSample, 0, count)
		for i := 0; i < count; i++ {
			samples = append(samples, ring[(next-count+i+len(ring))%len(ring)])
		}
		item := incident{Reason: pending.reason, TriggeredAt: pending.at, Samples: samples, DurationBucketUpperMicroseconds: durationBounds[:]}
		select {
		case r.files <- item:
		default:
			r.droppedFiles.Add(1)
		}
		pending = nil
	}
	defer flush()
	for {
		select {
		case <-ctx.Done():
			if pending == nil {
				select {
				case trigger := <-r.triggers:
					pending = &trigger
				default:
				}
			}
			// Preserve a final sample even when a failure is immediately followed
			// by shutdown, before the first one-second tick.
			ring[next] = r.sample(time.Now().UTC())
			next = (next + 1) % len(ring)
			if count < len(ring) {
				count++
			}
			return
		case trigger := <-r.triggers:
			if pending == nil && (lastIncident.IsZero() || trigger.at.Sub(lastIncident) >= time.Minute) {
				pending = &trigger
				lastIncident = trigger.at
			}
		case now := <-ticker.C:
			s := r.sample(now.UTC())
			ring[next] = s
			next = (next + 1) % len(ring)
			if count < len(ring) {
				count++
			}
			core := s.Pools["core"]
			if core.WaitCountDelta > 0 || (core.MaxOpen > 0 && core.InUse >= core.MaxOpen) {
				waitStreak++
			} else {
				waitStreak = 0
			}
			if waitStreak == 3 {
				r.Trigger("pool_wait")
			}
			if pending != nil && s.FinishedAt.Sub(pending.at) >= 20*time.Second {
				flush()
			}
		}
	}
}
func (r *Recorder) probeLoop(ctx context.Context) {
	defer close(r.probeDone)
	if r.probe == nil {
		return
	}
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		probeCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
		started := time.Now()
		sample := r.probe(probeCtx)
		sample.At = started.UTC()
		sample.FinishedAt = time.Now().UTC()
		sample.ElapsedMicroseconds = time.Since(started).Microseconds()
		cancel()
		r.probeMu.Lock()
		r.lastProbe = sample
		r.probeMu.Unlock()
	}
}
func (r *Recorder) writeLoop() {
	defer close(r.writerDone)
	for item := range r.files {
		if err := r.writeIncident(item); err != nil {
			r.fileFailures.Add(1)
			logger.L().Warn("database.diagnostic_write_failed", zap.String("error_class", "file_io"), zap.Uint64("failed_total", r.fileFailures.Load()), zap.Bool(logger.OpsSystemLogSkipField, true))
		}
	}
}
func (r *Recorder) writeIncident(item incident) error {
	data, err := json.Marshal(item)
	if err != nil {
		return err
	}
	// Inputs are bounded at collection (80 samples, 32 sessions, 16 blockers,
	// 16 KiB per /proc file). Retain the newest evidence if the encoded window
	// exceeds the disk budget rather than dropping the entire incident.
	for len(data) > 2<<20 && len(item.Samples) > 1 {
		n := max(1, len(item.Samples)/4)
		item.Samples = item.Samples[n:]
		item.OmittedSamples += n
		item.Truncated = true
		data, err = json.Marshal(item)
		if err != nil {
			return err
		}
	}
	if len(data) > 2<<20 {
		item.OmittedSamples += len(item.Samples)
		item.Samples = nil
		item.Truncated = true
		data, err = json.Marshal(item)
		if err != nil {
			return err
		}
	}
	// Eight fixed slots bound retention across restarts. Exclusive temporary files
	// and rename avoid following a preexisting final-file symlink.
	slot := item.TriggeredAt.Unix() / 60 % 8
	name := filepath.Join(r.dir, fmt.Sprintf(".incident-%d.tmp", slot))
	// Remove only this recorder's fixed temporary slot left by an interrupted
	// write. O_EXCL below rejects a concurrently planted replacement.
	if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(name) }()
	if _, err = temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(r.dir, fmt.Sprintf("incident-%d.json", slot)))
}
func (r *Recorder) Stop() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		activeRecorder.CompareAndSwap(r, nil)
		r.cancel()
		<-r.done
		close(r.files)
		// The SQL probe honors cancellation and has a bounded connect timeout.
		// Drain it before its pool is closed; the file writer is independent.
		<-r.probeDone
		select {
		case <-r.writerDone:
		case <-time.After(2 * time.Second):
		}
	})
}
