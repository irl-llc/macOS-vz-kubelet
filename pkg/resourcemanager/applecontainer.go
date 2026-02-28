package resourcemanager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	containerdata "github.com/agoda-com/macOS-vz-kubelet/internal/data/container"
	"github.com/agoda-com/macOS-vz-kubelet/internal/node"
	"github.com/agoda-com/macOS-vz-kubelet/internal/volumes"
	"github.com/agoda-com/macOS-vz-kubelet/pkg/event"
	"github.com/agoda-com/macOS-vz-kubelet/pkg/resource"

	"github.com/virtual-kubelet/virtual-kubelet/errdefs"
	"github.com/virtual-kubelet/virtual-kubelet/log"
	"github.com/virtual-kubelet/virtual-kubelet/node/api"
	"github.com/virtual-kubelet/virtual-kubelet/trace"
	stats "k8s.io/kubelet/pkg/apis/stats/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
)

// cpuSnapshot stores a single CPU reading for computing instantaneous usage rates.
type cpuSnapshot struct {
	nanos uint64
	time  time.Time
}

// AppleContainerClient manages containers via Apple's `container` CLI (macOS 26+).
type AppleContainerClient struct {
	cli           CLIExecutor
	eventRecorder event.EventRecorder
	data          containerdata.ContainerData

	cpuMu        sync.Mutex
	cpuSnapshots map[string]cpuSnapshot // keyed by "ns/pod/container"
}

// NewAppleContainerClient creates a new container client wrapping the Apple `container` CLI.
func NewAppleContainerClient(ctx context.Context, cli CLIExecutor, eventRecorder event.EventRecorder) (*AppleContainerClient, error) {
	ctx, span := trace.StartSpan(ctx, "AppleContainerClient.New")
	defer span.End()

	if cli == nil {
		return nil, fmt.Errorf("CLI executor is required")
	}

	client := &AppleContainerClient{
		cli:           cli,
		eventRecorder: eventRecorder,
		cpuSnapshots:  make(map[string]cpuSnapshot),
	}

	if err := client.cleanupDanglingContainers(ctx); err != nil {
		log.G(ctx).WithError(err).Warn("Failed to clean up dangling containers")
	}

	return client, nil
}

func (c *AppleContainerClient) cleanupDanglingContainers(ctx context.Context) error {
	ids, err := c.cli.ListContainers(ctx, ContainerNamePrefix)
	if err != nil {
		return err
	}
	for _, id := range ids {
		log.G(ctx).Infof("Removing dangling container %s", id)
		_ = c.cli.RemoveContainer(ctx, id, true)
	}
	return nil
}

// CreateContainer creates and starts a container via the Apple CLI.
func (c *AppleContainerClient) CreateContainer(ctx context.Context, params ContainerParams) (err error) {
	ctx, span := trace.StartSpan(ctx, "AppleContainerClient.CreateContainer")
	defer func() {
		span.SetStatus(err)
		span.End()
	}()

	_, loaded := c.data.GetOrCreateContainerInfo(
		params.PodNamespace, params.PodName, params.Name, containerdata.ContainerInfo{},
	)
	if loaded {
		return errdefs.AsInvalidInput(fmt.Errorf("container %s already exists", params.Name))
	}

	go c.handleContainerCreation(ctx, params)
	return nil
}

func (c *AppleContainerClient) handleContainerCreation(ctx context.Context, params ContainerParams) {
	var info containerdata.ContainerInfo
	var err error
	ctx, span := trace.StartSpan(ctx, "AppleContainerClient.handleContainerCreation")
	defer func() {
		span.SetStatus(err)
		span.End()
		if err != nil {
			c.data.SetContainerInfo(params.PodNamespace, params.PodName, params.Name, info.WithError(err))
		}
	}()

	if err = c.pullImageIfNeeded(ctx, params); err != nil {
		return
	}

	containerName := getUnderlyingContainerName(params.PodNamespace, params.PodName, params.Name)
	id, err := c.cli.CreateAndStartContainer(ctx, containerCreateArgs(params, containerName))
	if err != nil {
		c.eventRecorder.FailedToCreateContainer(ctx, params.Name, err)
		return
	}

	info = info.WithID(id)
	c.data.SetContainerInfo(params.PodNamespace, params.PodName, params.Name, info)
	c.eventRecorder.CreatedContainer(ctx, params.Name)
	c.eventRecorder.StartedContainer(ctx, params.Name)

	if params.PostStartAction == nil {
		return
	}

	if err = c.awaitAndExecPostStart(ctx, id, params); err != nil {
		c.eventRecorder.FailedPostStartHook(ctx, params.Name, params.PostStartAction.Command, err)
	}
}

func (c *AppleContainerClient) pullImageIfNeeded(ctx context.Context, params ContainerParams) error {
	switch params.ImagePullPolicy {
	case corev1.PullAlways:
		_ = c.cli.RemoveImage(ctx, params.Image)
		fallthrough
	case corev1.PullIfNotPresent:
		c.eventRecorder.PullingImage(ctx, params.Image, params.Name)
		start := time.Now()
		if err := c.cli.PullImage(ctx, params.Image, params.RegistryCreds); err != nil {
			c.eventRecorder.BackOffPullImage(ctx, params.Image, params.Name, err)
			return err
		}
		c.eventRecorder.PulledImage(ctx, params.Image, params.Name, time.Since(start).String())
	case corev1.PullNever:
		// skip
	}
	return nil
}

func (c *AppleContainerClient) awaitAndExecPostStart(ctx context.Context, containerID string, params ContainerParams) error {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			running, err := c.isRunningOrTerminal(ctx, containerID)
			if err != nil {
				return err
			}
			if !running {
				continue
			}
			return c.execPostStart(ctx, params)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *AppleContainerClient) isRunningOrTerminal(ctx context.Context, containerID string) (bool, error) {
	state, err := c.cli.InspectContainer(ctx, containerID)
	if err != nil {
		return false, err
	}
	if state.Status.IsTerminal() {
		return false, fmt.Errorf("container %s entered terminal state %d", containerID, state.Status)
	}
	return state.Status == resource.ContainerStatusRunning, nil
}

func (c *AppleContainerClient) execPostStart(ctx context.Context, params ContainerParams) error {
	timeout, cancel := context.WithTimeout(ctx, params.PostStartAction.TimeoutDuration)
	defer cancel()
	return c.ExecInContainer(timeout, params.PodNamespace, params.PodName, params.Name, params.PostStartAction.Command, node.DiscardingExecIO())
}

// WaitForContainer polls until a container reaches a terminal state and returns the exit code.
func (c *AppleContainerClient) WaitForContainer(ctx context.Context, podNs, podName, containerName string) (int, error) {
	info, ok := c.data.GetContainerInfo(podNs, podName, containerName)
	if !ok {
		return -1, errdefs.NotFound("container not found")
	}
	if info.Error != nil {
		return -1, info.Error
	}
	return c.pollUntilDone(ctx, info.ID)
}

func (c *AppleContainerClient) pollUntilDone(ctx context.Context, id string) (int, error) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			state, err := c.cli.InspectContainer(ctx, id)
			if err != nil {
				return -1, err
			}
			if state.Status.IsTerminal() {
				return state.ExitCode, nil
			}
		case <-ctx.Done():
			return -1, ctx.Err()
		}
	}
}

// RemoveContainer removes a single container for a pod.
func (c *AppleContainerClient) RemoveContainer(ctx context.Context, podNs, podName, containerName string) (err error) {
	ctx, span := trace.StartSpan(ctx, "AppleContainerClient.RemoveContainer")
	defer func() {
		span.SetStatus(err)
		span.End()
	}()

	info, ok := c.data.RemoveContainerInfo(podNs, podName, containerName)
	if !ok {
		return errdefs.NotFound("container not found")
	}
	if info.ID == "" {
		return nil
	}
	return c.cli.RemoveContainer(ctx, info.ID, true)
}

// RemoveContainers removes all containers for a pod.
func (c *AppleContainerClient) RemoveContainers(ctx context.Context, podNs, podName string, gracePeriod int64) (err error) {
	ctx, span := trace.StartSpan(ctx, "AppleContainerClient.RemoveContainers")
	defer func() {
		span.SetStatus(err)
		span.End()
	}()

	infoMap, loaded := c.data.RemoveAllContainerInfo(podNs, podName)
	if !loaded {
		return errdefs.NotFound("containers not found")
	}

	var errs []error
	for _, info := range infoMap {
		if info.ID == "" {
			continue
		}
		if err := c.cli.RemoveContainer(ctx, info.ID, true); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// GetContainers retrieves container state for a pod.
func (c *AppleContainerClient) GetContainers(ctx context.Context, podNs, podName string) ([]resource.Container, error) {
	ctx, span := trace.StartSpan(ctx, "AppleContainerClient.GetContainers")
	defer span.End()

	infoMap, ok := c.data.GetAllContainerInfo(podNs, podName)
	if !ok {
		return nil, errdefs.NotFound("containers not found")
	}
	return c.inspectAll(ctx, infoMap), nil
}

// GetContainersListResult returns containers for all tracked pods.
func (c *AppleContainerClient) GetContainersListResult(ctx context.Context) (map[k8stypes.NamespacedName][]resource.Container, error) {
	_, span := trace.StartSpan(ctx, "AppleContainerClient.GetContainersListResult")
	defer span.End()

	allData := c.data.GetAllData()
	result := make(map[k8stypes.NamespacedName][]resource.Container, len(allData))
	for key, infoMap := range allData {
		result[key] = c.inspectAll(ctx, infoMap)
	}
	return result, nil
}

func (c *AppleContainerClient) inspectAll(ctx context.Context, infoMap map[string]containerdata.ContainerInfo) []resource.Container {
	var (
		mu         sync.Mutex
		wg         sync.WaitGroup
		containers []resource.Container
	)
	for name, info := range infoMap {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctr := c.inspectOne(ctx, name, info)
			mu.Lock()
			containers = append(containers, ctr)
			mu.Unlock()
		}()
	}
	wg.Wait()
	sort.Slice(containers, func(i, j int) bool {
		return containers[i].Name < containers[j].Name
	})
	return containers
}

func (c *AppleContainerClient) inspectOne(ctx context.Context, name string, info containerdata.ContainerInfo) resource.Container {
	ctr := resource.Container{ID: info.ID, Name: name}
	if info.Error != nil {
		ctr.State.Error = info.Error.Error()
		return ctr
	}
	if info.ID == "" {
		return ctr
	}
	state, err := c.cli.InspectContainer(ctx, info.ID)
	if err != nil {
		ctr.State.Error = err.Error()
		return ctr
	}
	ctr.State = state
	return ctr
}

// GetContainerLogs retrieves logs from a container.
func (c *AppleContainerClient) GetContainerLogs(ctx context.Context, namespace, podName, containerName string, opts api.ContainerLogOpts) (io.ReadCloser, error) {
	ctx, span := trace.StartSpan(ctx, "AppleContainerClient.GetContainerLogs")
	defer span.End()

	name := getUnderlyingContainerName(namespace, podName, containerName)
	return c.cli.ContainerLogs(ctx, name, opts)
}

// ExecInContainer runs a command in a container.
func (c *AppleContainerClient) ExecInContainer(ctx context.Context, namespace, name, containerName string, cmd []string, attach api.AttachIO) (err error) {
	ctx, span := trace.StartSpan(ctx, "AppleContainerClient.ExecInContainer")
	defer func() {
		span.SetStatus(err)
		span.End()
	}()

	ctrName := getUnderlyingContainerName(namespace, name, containerName)
	return c.cli.ExecInContainer(ctx, ctrName, cmd, attach)
}

// AttachToContainer attaches to a running container.
func (c *AppleContainerClient) AttachToContainer(ctx context.Context, namespace, name, containerName string, attach api.AttachIO) (err error) {
	ctx, span := trace.StartSpan(ctx, "AppleContainerClient.AttachToContainer")
	defer func() {
		span.SetStatus(err)
		span.End()
	}()

	ctrName := getUnderlyingContainerName(namespace, name, containerName)
	return c.cli.AttachToContainer(ctx, ctrName, attach)
}

// IsContainerPresent checks if a container is tracked.
func (c *AppleContainerClient) IsContainerPresent(_ context.Context, podNs, podName, containerName string) bool {
	_, ok := c.data.GetContainerInfo(podNs, podName, containerName)
	return ok
}

// GetContainerStats returns resource usage stats for a container.
func (c *AppleContainerClient) GetContainerStats(ctx context.Context, podNs, podName, containerName string) (s stats.ContainerStats, err error) {
	ctx, span := trace.StartSpan(ctx, "AppleContainerClient.GetContainerStats")
	defer func() {
		span.SetStatus(err)
		span.End()
	}()

	info, exists := c.data.GetContainerInfo(podNs, podName, containerName)
	if !exists {
		return s, errdefs.NotFound("container not found")
	}
	if info.ID == "" {
		return s, errdefs.AsInvalidInput(fmt.Errorf("container not yet created"))
	}

	cpuNano, memBytes, err := c.cli.ContainerStats(ctx, info.ID)
	if err != nil {
		return s, err
	}

	now := time.Now()
	nanoCores := c.computeNanoCores(podNs, podName, containerName, cpuNano, now)
	metaNow := metav1.NewTime(now)
	return stats.ContainerStats{
		Name: containerName,
		CPU: &stats.CPUStats{
			Time:                 metaNow,
			UsageCoreNanoSeconds: &cpuNano,
			UsageNanoCores:       nanoCores,
		},
		Memory: &stats.MemoryStats{
			Time:            metaNow,
			UsageBytes:      &memBytes,
			WorkingSetBytes: &memBytes, // best approximation without cache breakdown
		},
	}, nil
}

// computeNanoCores derives instantaneous CPU usage from successive cumulative readings.
// Returns nil on the first call (no prior snapshot to diff against).
func (c *AppleContainerClient) computeNanoCores(podNs, podName, container string, cpuNano uint64, now time.Time) *uint64 {
	key := podNs + "/" + podName + "/" + container

	c.cpuMu.Lock()
	prev, hasPrev := c.cpuSnapshots[key]
	c.cpuSnapshots[key] = cpuSnapshot{nanos: cpuNano, time: now}
	c.cpuMu.Unlock()

	if !hasPrev {
		return nil
	}
	elapsed := now.Sub(prev.time)
	if elapsed <= 0 {
		return nil
	}
	rate := uint64(float64(cpuNano-prev.nanos) / elapsed.Seconds())
	return &rate
}

// containerCreateArgs builds the CLI arguments for container creation.
func containerCreateArgs(params ContainerParams, containerName string) ContainerCreateArgs {
	args := ContainerCreateArgs{
		Name:       containerName,
		Image:      params.Image,
		Env:        formatEnv(params.Env),
		Binds:      formatBinds(params.Mounts),
		Command:    params.Command,
		Args:       params.Args,
		WorkingDir: params.WorkingDir,
		TTY:        params.TTY,
		Stdin:      params.Stdin,
	}
	applySecurityContext(&args, params.SecurityContext)
	applyResources(&args, params.Resources)
	return args
}

func formatEnv(envVars []corev1.EnvVar) []string {
	env := make([]string, len(envVars))
	for i, e := range envVars {
		env[i] = e.Name + "=" + e.Value
	}
	return env
}

func formatBinds(mounts []volumes.Mount) []string {
	binds := make([]string, len(mounts))
	for i, m := range mounts {
		ro := "rw"
		if m.ReadOnly {
			ro = "ro"
		}
		binds[i] = fmt.Sprintf("%s:%s:%s", m.HostPath, m.ContainerPath, ro)
	}
	return binds
}

