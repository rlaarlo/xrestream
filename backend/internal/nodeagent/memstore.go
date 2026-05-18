package nodeagent

import (
	"context"
	"errors"
	"sync"

	"restream/backend/internal/store"
)

// memStore implements relay.ChannelStore for a remote node. Channels are
// populated by Agent.reconcile from /node/config. Status + metric writes
// are queued in reports for periodic flush back to the control plane.
type memStore struct {
	mu       sync.RWMutex
	bySlug   map[string]store.Channel
	byID     map[string]store.Channel
	statuses map[string]statusReport
	metrics  map[metricKey]int64
}

type statusReport struct {
	WorkerStatus string
	LastError    *string
	SourceStatus *int
	Dirty        bool
}

type metricKey struct {
	ChannelID string
	Field     string
}

func newMemStore() *memStore {
	return &memStore{
		bySlug:   map[string]store.Channel{},
		byID:     map[string]store.Channel{},
		statuses: map[string]statusReport{},
		metrics:  map[metricKey]int64{},
	}
}

// Replace swaps the active channel set atomically. Returns channels added and
// removed (by ID) so the caller can start/stop workers accordingly.
func (s *memStore) Replace(channels []store.Channel) (added, removed []store.Channel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := map[string]store.Channel{}
	nextBySlug := map[string]store.Channel{}
	for _, ch := range channels {
		next[ch.ID] = ch
		nextBySlug[ch.Slug] = ch
	}
	for id, ch := range next {
		if _, existed := s.byID[id]; !existed {
			added = append(added, ch)
		}
	}
	for id, ch := range s.byID {
		if _, still := next[id]; !still {
			removed = append(removed, ch)
		}
	}
	s.byID = next
	s.bySlug = nextBySlug
	return added, removed
}

// All returns a snapshot of the current channel set.
func (s *memStore) All() []store.Channel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]store.Channel, 0, len(s.byID))
	for _, ch := range s.byID {
		out = append(out, ch)
	}
	return out
}

// --- relay.ChannelStore --------------------------------------------------

var errNotFound = errors.New("channel not found")

func (s *memStore) GetChannelBySlug(_ context.Context, slug string) (store.Channel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ch, ok := s.bySlug[slug]
	if !ok {
		return store.Channel{}, errNotFound
	}
	return ch, nil
}

func (s *memStore) ActiveWorkerChannels(_ context.Context) ([]store.Channel, error) {
	return s.All(), nil
}

func (s *memStore) SetWorkerStatus(_ context.Context, id, workerStatus string, lastError *string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rep := s.statuses[id]
	rep.WorkerStatus = workerStatus
	rep.LastError = lastError
	rep.Dirty = true
	s.statuses[id] = rep
	return nil
}

func (s *memStore) SetSourceStatus(_ context.Context, id string, statusCode int, errMessage *string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rep := s.statuses[id]
	rep.SourceStatus = &statusCode
	if errMessage != nil {
		rep.LastError = errMessage
	}
	rep.Dirty = true
	s.statuses[id] = rep
	return nil
}

func (s *memStore) TouchRequest(_ context.Context, _ string) {
	// no-op on the node; the control plane infers last_request_at from
	// segment_requests increments which arrive via /node/report.
}

func (s *memStore) IncrementMetric(_ context.Context, channelID, field string, amount int64) {
	if channelID == "" || field == "" || amount == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metrics[metricKey{ChannelID: channelID, Field: field}] += amount
}

// DrainReports returns + clears all queued status/metric reports.
func (s *memStore) DrainReports() (statuses map[string]statusReport, metrics map[metricKey]int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	statuses = map[string]statusReport{}
	for id, rep := range s.statuses {
		if rep.Dirty {
			statuses[id] = rep
		}
	}
	s.statuses = map[string]statusReport{}
	metrics = s.metrics
	s.metrics = map[metricKey]int64{}
	return statuses, metrics
}
