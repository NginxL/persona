package assistant

import "sync"

// A table of per-key locks. The Inbox's message cards hold one around "Send"; the tasks keep tables of their own for
// the day's card, plan edits and reviews (tasks.go, task_review_dismissals.go).

// cardLocks are the locks of the message cards whose "Send" is going on or waited for (sendFromCard). An entry lives
// while someone holds or waits on its lock, so the assistant keeps none for the cards it is done with. The count makes
// letting go safe: a tap waiting on a card's lock keeps that lock in the table, and a tap that comes later shares it
// rather than making a second lock that would let both sends run at once.
type cardLocks struct {
	mu sync.Mutex
	m  map[string]*cardLock
}

// cardLock is one card's lock and how many hold or wait on it (under cardLocks.mu).
type cardLock struct {
	mu sync.Mutex
	n  int
}

// acquire takes card id's lock, waiting for whoever holds it.
func (c *cardLocks) acquire(id string) {
	c.mu.Lock()
	if c.m == nil {
		c.m = map[string]*cardLock{}
	}
	l := c.m[id]
	if l == nil {
		l = &cardLock{}
		c.m[id] = l
	}
	l.n++
	c.mu.Unlock()
	l.mu.Lock()
}

// release lets card id's lock go (taken with acquire), forgetting it when nobody else holds or waits on it.
func (c *cardLocks) release(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	l := c.m[id]
	l.mu.Unlock()
	if l.n--; l.n == 0 {
		delete(c.m, id)
	}
}
