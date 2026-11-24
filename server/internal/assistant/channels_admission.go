package assistant

import (
	"context"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/channel"
)

type channelAdmissionKey struct {
	channel, thread string
	generation      uint64
}

type channelAdmission struct {
	key        channelAdmissionKey
	prev, done chan struct{}
}

// admitChannelMessage claims the message, captures its binding generation and reserves its position atomically.
// No node or channel RPC runs under the book lock. Reserve before persisting the claim so a later command cannot
// overtake a durable receipt. A failed write rolls both changes back for redelivery.
func (s *Service) admitChannelMessage(id string, m channel.Message) (*channelAdmission, error) {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	s.loadChannelStateLocked(context.Background())
	if !c.stLoaded {
		return nil, errChannelsUnread
	}
	seen, hadSeen := c.st.Seen[id]
	if m.ID == "" || slices.Contains(seen, m.ID) {
		return nil, nil
	}
	a := &channelAdmission{key: channelAdmissionKey{channel: id, thread: strings.TrimSpace(m.Thread), generation: c.generation[id]}}
	if a.key.thread != "" {
		if c.receiving == nil {
			c.receiving = map[channelAdmissionKey]chan struct{}{}
		}
		a.prev = c.receiving[a.key]
		a.done = make(chan struct{})
		c.receiving[a.key] = a.done
	}
	next := append(seen, m.ID)
	if len(next) > channelSeenKeep {
		next = next[len(next)-channelSeenKeep:]
	}
	c.st.Seen[id] = next
	if err := s.saveChannelStateLocked(); err != nil {
		if hadSeen {
			c.st.Seen[id] = seen
		} else {
			delete(c.st.Seen, id)
		}
		if a.done != nil {
			if a.prev == nil {
				delete(c.receiving, a.key)
			} else {
				c.receiving[a.key] = a.prev
			}
		}
		return nil, err
	}
	return a, nil
}

// run keeps one binding generation's thread ordered. A new binding never waits for an old generation's RPC.
// The routing code still gates destinations on the channel's threads capability.
func (a *channelAdmission) run(s *Service, m channel.Message, action string) {
	if a.done != nil {
		defer func() {
			c := &s.chans
			c.mu.Lock()
			if c.receiving[a.key] == a.done {
				delete(c.receiving, a.key)
			}
			close(a.done)
			c.mu.Unlock()
		}()
	}
	if a.prev != nil {
		<-a.prev
	}
	s.channelReceiveInGeneration(a.key.channel, m, action, a.key.generation)
}

func (s *Service) channelGeneration(id string) uint64 {
	s.chans.mu.Lock()
	defer s.chans.mu.Unlock()
	return s.chans.generation[id]
}

func (s *Service) channelGenerationCurrent(id string, generation uint64) bool {
	return s.channelGeneration(id) == generation
}
