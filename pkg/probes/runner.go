package probes

import (
	"context"
	"sync"
	"time"

	"github.com/virtual-kubelet/virtual-kubelet/log"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Runner manages probe goroutines for all containers.
type Runner struct {
	Results *ResultStore

	exec     ExecRunner
	resolver IPResolver

	mu        sync.Mutex
	cancels   map[probeKey]context.CancelFunc
	startedCh map[startedKey]chan struct{}
}

// startedKey identifies a container's startup-complete signal.
type startedKey struct {
	pod       types.NamespacedName
	container string
}

// NewRunner creates a probe runner with the given exec and IP resolution callbacks.
func NewRunner(exec ExecRunner, resolver IPResolver) *Runner {
	return &Runner{
		Results:   NewResultStore(),
		exec:      exec,
		resolver:  resolver,
		cancels:   make(map[probeKey]context.CancelFunc),
		startedCh: make(map[startedKey]chan struct{}),
	}
}

// StartForPod launches probe goroutines for every probed container in a pod.
func (r *Runner) StartForPod(pod *corev1.Pod) {
	nn := types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}
	log.L.Infof("starting probes for pod %s", nn)
	for _, ctr := range pod.Spec.Containers {
		gate := r.startupGate(nn, ctr)
		r.startProbe(nn, ctr, Startup, ctr.StartupProbe, nil)
		r.startProbe(nn, ctr, Liveness, ctr.LivenessProbe, gate)
		r.startProbe(nn, ctr, Readiness, ctr.ReadinessProbe, gate)
	}
}

// startupGate returns a channel that closes when the startup probe passes,
// or nil if no startup probe is configured.
func (r *Runner) startupGate(nn types.NamespacedName, ctr corev1.Container) chan struct{} {
	if ctr.StartupProbe == nil {
		return nil
	}
	ch := make(chan struct{})
	key := startedKey{pod: nn, container: ctr.Name}
	r.mu.Lock()
	r.startedCh[key] = ch
	r.mu.Unlock()
	return ch
}

func (r *Runner) startProbe(
	nn types.NamespacedName, ctr corev1.Container,
	pt ProbeType, probe *corev1.Probe, gate chan struct{},
) {
	if probe == nil {
		return
	}
	key := probeKey{pod: nn, container: ctr.Name, probeType: pt}
	ctx, cancel := context.WithCancel(context.Background())

	r.mu.Lock()
	r.cancels[key] = cancel
	r.mu.Unlock()

	spec := probeSpec{
		probe:     probe,
		pod:       nn,
		container: ctr.Name,
		probeType: pt,
	}
	go r.loop(ctx, spec, gate)
}

type probeSpec struct {
	probe     *corev1.Probe
	pod       types.NamespacedName
	container string
	probeType ProbeType
}

func (r *Runner) loop(ctx context.Context, spec probeSpec, gate chan struct{}) {
	if !r.awaitGate(ctx, gate) {
		return
	}

	delay := time.Duration(spec.probe.InitialDelaySeconds) * time.Second
	period := probePeriod(spec.probe)
	timeout := probeTimeout(spec.probe)

	if !sleep(ctx, delay) {
		return
	}
	for {
		outcome := r.execute(ctx, spec, timeout)
		r.Results.Record(spec.pod, spec.container, spec.probeType, outcome)
		r.notifyStartup(spec, outcome)
		log.L.Debugf("probe %s/%s %s: %s", spec.pod, spec.container, spec.probeType, outcome)
		if !sleep(ctx, period) {
			return
		}
	}
}

// awaitGate blocks until gate closes or ctx is cancelled.
// Returns false if context cancelled first.
func (r *Runner) awaitGate(ctx context.Context, gate chan struct{}) bool {
	if gate == nil {
		return true
	}
	select {
	case <-gate:
		return true
	case <-ctx.Done():
		return false
	}
}

// notifyStartup closes the startup channel when a startup probe first succeeds.
func (r *Runner) notifyStartup(spec probeSpec, outcome Outcome) {
	if spec.probeType != Startup || outcome != Success {
		return
	}
	threshold := successThreshold(spec.probe)
	if !r.Results.Passing(spec.pod, spec.container, Startup, threshold) {
		return
	}
	key := startedKey{pod: spec.pod, container: spec.container}
	r.mu.Lock()
	defer r.mu.Unlock()
	if ch, ok := r.startedCh[key]; ok {
		select {
		case <-ch: // already closed
		default:
			close(ch)
		}
	}
}

func (r *Runner) execute(ctx context.Context, spec probeSpec, timeout time.Duration) Outcome {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	p := spec.probe
	ns, pod, ctr := spec.pod.Namespace, spec.pod.Name, spec.container

	if p.Exec != nil {
		return RunExecProbe(ctx, r.exec, ns, pod, ctr, p.Exec)
	}
	if p.HTTPGet != nil {
		return RunHTTPProbe(ctx, r.resolver, ns, pod, ctr, p.HTTPGet, timeout)
	}
	if p.TCPSocket != nil {
		return RunTCPProbe(ctx, r.resolver, ns, pod, ctr, p.TCPSocket, timeout)
	}
	return Unknown
}

// StopForPod cancels all probe goroutines for a pod and clears stored results.
func (r *Runner) StopForPod(pod types.NamespacedName) {
	log.L.Infof("stopping probes for pod %s", pod)
	r.mu.Lock()
	for key, cancel := range r.cancels {
		if key.pod == pod {
			cancel()
			delete(r.cancels, key)
		}
	}
	for key := range r.startedCh {
		if key.pod == pod {
			delete(r.startedCh, key)
		}
	}
	r.mu.Unlock()
	r.Results.Remove(pod)
}

func probePeriod(p *corev1.Probe) time.Duration {
	if p.PeriodSeconds > 0 {
		return time.Duration(p.PeriodSeconds) * time.Second
	}
	return 10 * time.Second
}

func probeTimeout(p *corev1.Probe) time.Duration {
	if p.TimeoutSeconds > 0 {
		return time.Duration(p.TimeoutSeconds) * time.Second
	}
	return 1 * time.Second
}

// sleep waits for the duration or until the context is cancelled.
// Returns false if the context was cancelled.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
