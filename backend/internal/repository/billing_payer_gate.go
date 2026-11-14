package repository

import (
	"container/list"
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/dbstats"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

var errBillingPayerQueueFull = errors.New("billing payer queue full")

type billingPayerKey struct {
	db     *sql.DB
	userID int64
}
type billingPayerWaiter struct {
	ready   chan struct{}
	granted bool
	node    *list.Element
}
type billingPayerEntry struct{ waiting list.List }
type billingPayerGate struct {
	mu                                        sync.Mutex
	entries                                   map[billingPayerKey]*billingPayerEntry
	changed                                   chan struct{}
	waiting, maxKeys, maxWaiting, maxPerPayer int
}

func newBillingPayerGate(keys, waiting, perPayer int) *billingPayerGate {
	return &billingPayerGate{entries: make(map[billingPayerKey]*billingPayerEntry), changed: make(chan struct{}), maxKeys: keys, maxWaiting: waiting, maxPerPayer: perPayer}
}

// Shared by repository instances, including recovery workers. This supplements
// PostgreSQL locks; it never replaces their cross-instance correctness.
var sharedBillingPayerGate = newBillingPayerGate(1024, 4096, 128)

func (g *billingPayerGate) notifyLocked() { close(g.changed); g.changed = make(chan struct{}) }

func (g *billingPayerGate) acquire(ctx context.Context, key billingPayerKey, waitForCapacity bool) (func(), error) {
	for {
		g.mu.Lock()
		if err := ctx.Err(); err != nil {
			g.mu.Unlock()
			return nil, err
		}
		entry := g.entries[key]
		if entry == nil && len(g.entries) < g.maxKeys {
			entry = &billingPayerEntry{}
			g.entries[key] = entry
			g.mu.Unlock()
			return g.lease(key, entry), nil
		}
		if entry == nil || entry.waiting.Len() >= g.maxPerPayer || g.waiting >= g.maxWaiting {
			changed := g.changed
			g.mu.Unlock()
			if !waitForCapacity {
				return nil, errBillingPayerQueueFull
			}
			// Legacy Apply has no durable replay record. Do not introduce a new
			// fail-fast loss path: wait for queue capacity using its original
			// deadline, without allocating a queue node or borrowing a connection.
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-changed:
				continue
			}
		}
		waiter := &billingPayerWaiter{ready: make(chan struct{})}
		waiter.node = entry.waiting.PushBack(waiter)
		g.waiting++
		g.mu.Unlock()
		select {
		case <-waiter.ready:
			release := g.lease(key, entry)
			if err := ctx.Err(); err != nil {
				release()
				return nil, err
			}
			return release, nil
		case <-ctx.Done():
			g.mu.Lock()
			granted := waiter.granted
			if !granted {
				entry.waiting.Remove(waiter.node)
				g.waiting--
				g.notifyLocked()
			}
			g.mu.Unlock()
			if granted {
				g.lease(key, entry)()
			}
			return nil, ctx.Err()
		}
	}
}

func (g *billingPayerGate) lease(key billingPayerKey, entry *billingPayerEntry) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			if front := entry.waiting.Front(); front != nil {
				waiter, ok := front.Value.(*billingPayerWaiter)
				if !ok {
					panic("billing payer queue contains a non-waiter")
				}
				entry.waiting.Remove(front)
				g.waiting--
				waiter.granted = true
				close(waiter.ready)
			} else {
				delete(g.entries, key)
			}
			g.notifyLocked()
		})
	}
}

func beginPayerBillingTransaction(ctx context.Context, db *sql.DB, userID int64, waitForCapacity bool) (*sql.Tx, func(), error) {
	started := time.Now()
	leave, err := sharedBillingPayerGate.acquire(ctx, billingPayerKey{db: db, userID: userID}, waitForCapacity)
	dbstats.Record("payer_admission", time.Since(started), err)
	if err != nil {
		return nil, nil, usageBillingStageError("payer_admission", started, err)
	}
	tx, cleanup, err := beginUsageBillingTransaction(ctx, db)
	if err != nil {
		leave()
		return nil, nil, err
	}
	return tx, func() { cleanup(); leave() }, nil
}

// This indexed, non-locking lookup returns its connection before waiting. The
// immutable settlement owner is rechecked under the transaction's row lock.
func (r *usageBillingRepository) settlementPayer(ctx context.Context, ref service.UsageSettlementRef) (int64, error) {
	var userID int64
	err := r.db.QueryRowContext(ctx, `SELECT user_id FROM usage_settlements WHERE request_id=$1 AND api_key_id=$2`, ref.RequestID, ref.APIKeyID).Scan(&userID)
	return userID, err
}
