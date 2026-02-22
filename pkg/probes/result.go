package probes

import (
	"sync"

	"k8s.io/apimachinery/pkg/types"
)

// ProbeType distinguishes liveness, readiness, and startup probes.
type ProbeType int

const (
	Liveness ProbeType = iota
	Readiness
	Startup
)

// String returns a human-readable label for the probe type.
func (pt ProbeType) String() string {
	switch pt {
	case Liveness:
		return "liveness"
	case Readiness:
		return "readiness"
	case Startup:
		return "startup"
	default:
		return "unknown"
	}
}

// Outcome represents the result of a single probe execution.
type Outcome int

const (
	Success Outcome = iota
	Failure
	Unknown
)

// String returns a human-readable label for the outcome.
func (o Outcome) String() string {
	switch o {
	case Success:
		return "success"
	case Failure:
		return "failure"
	default:
		return "unknown"
	}
}

// probeKey uniquely identifies a probe for a specific container.
type probeKey struct {
	pod       types.NamespacedName
	container string
	probeType ProbeType
}

// ResultStore tracks probe outcomes with thread-safe access.
type ResultStore struct {
	mu      sync.RWMutex
	results map[probeKey]*probeState
}

type probeState struct {
	consecutiveSuccess int
	consecutiveFailure int
	lastOutcome        Outcome
}

// NewResultStore creates an empty probe result store.
func NewResultStore() *ResultStore {
	return &ResultStore{results: make(map[probeKey]*probeState)}
}

// Record updates the probe state with a new outcome.
func (s *ResultStore) Record(pod types.NamespacedName, container string, pt ProbeType, outcome Outcome) {
	key := probeKey{pod: pod, container: container, probeType: pt}
	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.getOrCreate(key)
	state.lastOutcome = outcome
	if outcome == Success {
		state.consecutiveSuccess++
		state.consecutiveFailure = 0
		return
	}
	state.consecutiveFailure++
	state.consecutiveSuccess = 0
}

func (s *ResultStore) getOrCreate(key probeKey) *probeState {
	if st, ok := s.results[key]; ok {
		return st
	}
	st := &probeState{}
	s.results[key] = st
	return st
}

// Passing reports whether a probe has met its success threshold.
func (s *ResultStore) Passing(pod types.NamespacedName, container string, pt ProbeType, threshold int) bool {
	key := probeKey{pod: pod, container: container, probeType: pt}
	s.mu.RLock()
	defer s.mu.RUnlock()

	st, ok := s.results[key]
	if !ok {
		return false
	}
	return st.consecutiveSuccess >= threshold
}

// Failing reports whether a probe has met its failure threshold.
func (s *ResultStore) Failing(pod types.NamespacedName, container string, pt ProbeType, threshold int) bool {
	key := probeKey{pod: pod, container: container, probeType: pt}
	s.mu.RLock()
	defer s.mu.RUnlock()

	st, ok := s.results[key]
	if !ok {
		return false
	}
	return st.consecutiveFailure >= threshold
}

// Remove deletes all probe state for a given pod.
func (s *ResultStore) Remove(pod types.NamespacedName) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for key := range s.results {
		if key.pod == pod {
			delete(s.results, key)
		}
	}
}
