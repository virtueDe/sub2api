package httputil

import (
	"context"
	"fmt"
	"sync"
)

const (
	imageMemoryExpansion      = int64(16)
	imageMemoryFixed          = int64(2 << 20)
	imageMemoryMaxWaiting     = 256
	imageMemoryMaxUserWaiting = 128
)

type imageMemoryWaiter struct {
	user, bytes int64
	ready       chan struct{}
	granted     bool
}
type imageMemoryQueue struct {
	users   map[int64][]*imageMemoryWaiter
	order   []int64
	waiting int
	used    int64
}

func (b *BodyMemoryBudget) imageLimit() int64 {
	// Keep a share of the existing budget available to non-image requests.
	return b.limit - min(b.limit/8, 64<<20)
}

// AdmitImageRequestMemory waits before the first body read. The envelope covers
// ingress, retained parsed fields, audit JSON/data URLs and a conversion attempt.
// Unknown or encoded lengths take the largest envelope; actual reads still use
// MaxBytesReader and the byte budget, never Content-Length as an EOF boundary.
func AdmitImageRequestMemory(ctx context.Context, userID, contentLength int64, encoded bool) (context.Context, func(), error) {
	parent, _ := ctx.Value(bodyMemoryReservationKey{}).(*bodyMemoryReservation)
	if parent == nil {
		return ctx, func() {}, nil
	}
	parent.mu.Lock()
	b, closed, alreadyRead := parent.budget, parent.closed, parent.bytes != 0
	parent.mu.Unlock()
	if b.onLastOwner != nil {
		return ctx, func() {}, nil
	} // Detached execution owns its admission.
	if closed || alreadyRead {
		return ctx, nil, ErrRequestBodyMemoryBudget
	}
	amount := b.imageLimit()
	if contentLength >= 0 && !encoded {
		if contentLength > amount {
			return ctx, nil, ErrRequestBodyMemoryBudget
		}
		if contentLength <= (amount-imageMemoryFixed)/imageMemoryExpansion {
			amount = contentLength*imageMemoryExpansion + imageMemoryFixed
		}
	}
	leave, err := b.acquireImageMemory(ctx, userID, amount)
	if err != nil {
		return ctx, nil, err
	}
	working := NewBodyMemoryBudget(amount)
	working.owners, working.parentImageBudget = 1, b
	working.onLastOwner = func() {
		// A URL download or dishonest Content-Length can need additional bytes.
		// Growth is nonblocking and bounded by the same global budget.
		working.mu.Lock()
		extra := working.limit - amount
		working.mu.Unlock()
		if extra > 0 {
			b.mu.Lock()
			b.used -= extra
			b.images.used -= extra
			b.scheduleImagesLocked()
			b.mu.Unlock()
		}
		leave()
	}
	r := &bodyMemoryReservation{budget: working}
	return context.WithValue(ctx, bodyMemoryReservationKey{}, r), r.release, nil
}

func (b *BodyMemoryBudget) acquireImageMemory(ctx context.Context, userID, amount int64) (func(), error) {
	b.mu.Lock()
	if err := ctx.Err(); err != nil {
		b.mu.Unlock()
		return nil, err
	}
	if amount <= 0 || amount > b.imageLimit() {
		b.mu.Unlock()
		return nil, ErrRequestBodyMemoryBudget
	}
	if b.images == nil {
		b.images = &imageMemoryQueue{users: make(map[int64][]*imageMemoryWaiter)}
	}
	q := b.images
	if q.waiting >= imageMemoryMaxWaiting || len(q.users[userID]) >= imageMemoryMaxUserWaiting {
		b.mu.Unlock()
		return nil, ErrRequestBodyMemoryBudget
	}
	w := &imageMemoryWaiter{user: userID, bytes: amount, ready: make(chan struct{})}
	if len(q.users[userID]) == 0 {
		q.order = append(q.order, userID)
	}
	q.users[userID] = append(q.users[userID], w)
	q.waiting++
	b.scheduleImagesLocked()
	b.mu.Unlock()
	select {
	case <-w.ready:
		leave := b.imageLease(amount)
		if err := ctx.Err(); err != nil {
			leave()
			return nil, err
		}
		return leave, nil
	case <-ctx.Done():
		b.mu.Lock()
		granted := w.granted
		if !granted {
			waiters := q.users[userID]
			for i, other := range waiters {
				if other == w {
					waiters = append(waiters[:i], waiters[i+1:]...)
					break
				}
			}
			q.waiting--
			if len(waiters) == 0 {
				delete(q.users, userID)
				for i, u := range q.order {
					if u == userID {
						q.order = append(q.order[:i], q.order[i+1:]...)
						break
					}
				}
			} else {
				q.users[userID] = waiters
			}
			b.scheduleImagesLocked()
		}
		b.mu.Unlock()
		if granted {
			b.imageLease(amount)()
		}
		return nil, ctx.Err()
	}
}

// Round robin between users, FIFO within one user. An older large request is
// allowed to accumulate capacity; later small requests cannot starve it forever.
func (b *BodyMemoryBudget) scheduleImagesLocked() {
	q := b.images
	if q == nil {
		return
	}
	for len(q.order) > 0 {
		user := q.order[0]
		w := q.users[user][0]
		if w.bytes > b.limit-b.used || w.bytes > b.imageLimit()-q.used {
			return
		}
		q.order = q.order[1:]
		waiters := q.users[user][1:]
		if len(waiters) > 0 {
			q.users[user] = waiters
			q.order = append(q.order, user)
		} else {
			delete(q.users, user)
		}
		q.waiting--
		q.used += w.bytes
		b.used += w.bytes
		w.granted = true
		close(w.ready)
	}
}

func (b *BodyMemoryBudget) imageLease(amount int64) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			b.used -= amount
			b.images.used -= amount
			b.scheduleImagesLocked()
			b.mu.Unlock()
		})
	}
}

// The caller holds the private budget mutex. Never wait here: holding raw data
// while waiting for another conversion grant could deadlock all admitted work.
func (b *BodyMemoryBudget) growImageBudgetLocked(extra int64) bool {
	parent := b.parentImageBudget
	if parent == nil {
		return false
	}
	parent.mu.Lock()
	defer parent.mu.Unlock()
	if extra > parent.limit-parent.used || extra > parent.imageLimit()-parent.images.used {
		return false
	}
	parent.used += extra
	parent.images.used += extra
	b.limit += extra
	return true
}

// ReserveImageWorkingMemory accounts conservative peak byte representations
// before parsing/encoding. It never waits while holding a raw request. Separate
// attempt/audit scopes refund their bytes, so retries cannot accumulate charges.
func ReserveImageWorkingMemory(ctx context.Context, bodyBytes int, copies int64) error {
	if bodyBytes < 0 || copies < 0 || int64(bodyBytes) > (int64(^uint64(0)>>1)-(64<<10))/max(copies, 1) {
		return fmt.Errorf("image representation: %w", ErrRequestBodyMemoryBudget)
	}
	return ReserveRequestBodyMemory(ctx, int64(bodyBytes)*copies+(64<<10))
}
