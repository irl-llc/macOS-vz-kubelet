package probes

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"
)

var testPod = types.NamespacedName{Namespace: "ns", Name: "pod"}

func TestResultStore_RecordSuccess(t *testing.T) {
	s := NewResultStore()
	s.Record(testPod, "c1", Readiness, Success)
	assert.True(t, s.Passing(testPod, "c1", Readiness, 1))
	assert.False(t, s.Failing(testPod, "c1", Readiness, 1))
}

func TestResultStore_RecordFailure(t *testing.T) {
	s := NewResultStore()
	s.Record(testPod, "c1", Liveness, Failure)
	assert.True(t, s.Failing(testPod, "c1", Liveness, 1))
	assert.False(t, s.Passing(testPod, "c1", Liveness, 1))
}

func TestResultStore_ThresholdNotMet(t *testing.T) {
	s := NewResultStore()
	s.Record(testPod, "c1", Readiness, Success)
	assert.False(t, s.Passing(testPod, "c1", Readiness, 3))
}

func TestResultStore_ThresholdMet(t *testing.T) {
	s := NewResultStore()
	for range 3 {
		s.Record(testPod, "c1", Readiness, Success)
	}
	assert.True(t, s.Passing(testPod, "c1", Readiness, 3))
}

func TestResultStore_FailureResetsSuccess(t *testing.T) {
	s := NewResultStore()
	s.Record(testPod, "c1", Readiness, Success)
	s.Record(testPod, "c1", Readiness, Success)
	s.Record(testPod, "c1", Readiness, Failure)
	assert.False(t, s.Passing(testPod, "c1", Readiness, 1))
	assert.True(t, s.Failing(testPod, "c1", Readiness, 1))
}

func TestResultStore_SuccessResetsFailure(t *testing.T) {
	s := NewResultStore()
	s.Record(testPod, "c1", Liveness, Failure)
	s.Record(testPod, "c1", Liveness, Failure)
	s.Record(testPod, "c1", Liveness, Success)
	assert.True(t, s.Passing(testPod, "c1", Liveness, 1))
	assert.False(t, s.Failing(testPod, "c1", Liveness, 1))
}

func TestResultStore_UnknownPod(t *testing.T) {
	s := NewResultStore()
	unknown := types.NamespacedName{Namespace: "ns", Name: "other"}
	assert.False(t, s.Passing(unknown, "c1", Readiness, 1))
	assert.False(t, s.Failing(unknown, "c1", Readiness, 1))
}

func TestResultStore_Remove(t *testing.T) {
	s := NewResultStore()
	s.Record(testPod, "c1", Readiness, Success)
	s.Record(testPod, "c2", Liveness, Success)
	s.Remove(testPod)
	assert.False(t, s.Passing(testPod, "c1", Readiness, 1))
	assert.False(t, s.Passing(testPod, "c2", Liveness, 1))
}

func TestResultStore_IsolateContainers(t *testing.T) {
	s := NewResultStore()
	s.Record(testPod, "c1", Readiness, Success)
	s.Record(testPod, "c2", Readiness, Failure)
	assert.True(t, s.Passing(testPod, "c1", Readiness, 1))
	assert.False(t, s.Passing(testPod, "c2", Readiness, 1))
}
