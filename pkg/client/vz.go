package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/agoda-com/macOS-vz-kubelet/internal/utils"
	"github.com/agoda-com/macOS-vz-kubelet/internal/volumes"
	"github.com/agoda-com/macOS-vz-kubelet/pkg/event"
	"github.com/agoda-com/macOS-vz-kubelet/pkg/resource"
	rm "github.com/agoda-com/macOS-vz-kubelet/pkg/resourcemanager"
	"github.com/agoda-com/macOS-vz-kubelet/pkg/vm"

	"github.com/virtual-kubelet/virtual-kubelet/errdefs"
	"github.com/virtual-kubelet/virtual-kubelet/log"
	"github.com/virtual-kubelet/virtual-kubelet/node/api"
	"github.com/virtual-kubelet/virtual-kubelet/trace"
	stats "k8s.io/kubelet/pkg/apis/stats/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// PodMountsDir is the directory where pod volumes are mounted.
	// It is createdinside the cache directory.
	PodMountsDir = "mounts"

	// Timeout for executing post-start command.
	//
	// @note: While pre-stop hook timeout is suppored by simply setting terminationGracePeriodSeconds,
	// as of now k8s does not support setting custom timeout for post-start command at all.
	// Setting this constant as default for now (usually post-start should be something lite anyway).
	PostStartCommandTimeout = 10 * time.Second

	// DefaultInitTimeout caps how long all init containers may run before
	// the pod is considered failed. Overridden by pod.Spec.ActiveDeadlineSeconds.
	DefaultInitTimeout = 10 * time.Minute
)

var (
	// errVirtualizationGroupNotFound is returned when a virtualization group is not found.
	errVirtualizationGroupNotFound = errdefs.NotFound("virtualization group not found")
)

// virtualizationGroupExtras contains additional information for a virtualization group.
type virtualizationGroupExtras struct {
	rootDir     string             // root directory for the volumes of the pod
	cancelFunc  context.CancelFunc // context cancellation function for the virtualization group
	networkName string             // per-pod vmnet network (empty if no native containers)

	initStates []resource.Container // final states of completed init containers

	deleteOnce sync.Once  // ensures that the virtualization group is deleted only once
	deleteDone chan error // signals that the virtualization group has been deleted
}

// VzClientAPIs is a concrete implementation of VzClientInterface, using MacOSClient and ContainerClient.
type VzClientAPIs struct {
	MacOSClient     *rm.MacOSClient
	ContainerClient rm.ContainersClient // Optional

	cachePath  string
	clusterDNS string   // cluster DNS IP for service discovery
	extras     sync.Map // map[types.NamespacedName]*virtualizationGroupExtras
}

// NewVzClientAPIs initializes and returns a new VzClientAPIs instance.
func NewVzClientAPIs(ctx context.Context, eventRecorder event.EventRecorder, networkInterfaceIdentifier, cachePath, clusterDNS string, containerClient rm.ContainersClient) *VzClientAPIs {
	ctx, span := trace.StartSpan(ctx, "VZClient.NewVzClientAPIs")
	defer span.End()

	// force remove dangling mounts
	_ = os.RemoveAll(filepath.Join(cachePath, PodMountsDir))

	return &VzClientAPIs{
		MacOSClient:     rm.NewMacOSClient(ctx, eventRecorder, networkInterfaceIdentifier, cachePath),
		ContainerClient: containerClient,
		cachePath:       cachePath,
		clusterDNS:      clusterDNS,
	}
}

// CreateVirtualizationGroup creates a new virtualization group based on the provided Kubernetes pod.
func (c *VzClientAPIs) CreateVirtualizationGroup(ctx context.Context, pod *corev1.Pod, volData *volumes.PodVolumeData, creds resource.RegistryCredentialStore) (err error) {
	ctx, span := trace.StartSpan(ctx, "VZClient.CreateVirtualizationGroup")
	key := types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}
	extras := &virtualizationGroupExtras{
		rootDir:    c.getPodVolumeRoot(pod),
		deleteDone: make(chan error, 1),
	}
	defer func() {
		span.SetStatus(err)
		span.End()

		// cleanup if an error occurred
		if err != nil {
			c.extras.Delete(key)
			if err := os.RemoveAll(extras.rootDir); err != nil {
				log.G(ctx).WithError(err).Warn("Failed to clean up pod volume root")
			}
			if extras.cancelFunc != nil {
				extras.cancelFunc()
			}
		}
	}()

	vmNames := VMContainerNames(pod)
	log.G(ctx).Debugf("pod %s/%s runtime routing: vm=%v", pod.Namespace, pod.Name, vmNames)
	hasNative := hasNativeContainers(pod, vmNames) || len(pod.Spec.InitContainers) > 0

	if len(vmNames) > 1 {
		return errdefs.InvalidInput("at most one VM container per pod is supported")
	}

	if hasNative && c.ContainerClient == nil {
		return errdefs.InvalidInput("native containers require a container client")
	}

	// Due to the nature of virtual kubelet CreatePod context,
	// we need to handle the context cancellation on demand ourselves
	ctx, extras.cancelFunc = context.WithCancel(ctx)

	// Create per-pod vmnet network for native containers
	if hasNative && c.ContainerClient != nil {
		netName := rm.PodNetworkName(string(pod.UID))
		if err = c.ContainerClient.CreatePodNetwork(ctx, netName); err != nil {
			return fmt.Errorf("create pod network: %w", err)
		}
		extras.networkName = netName
	}

	// Store the extras for the virtualization group before doing any async work
	c.extras.Store(key, extras)

	// Run init containers sequentially — each must complete before the next starts
	if err = c.runInitContainers(ctx, pod, extras, volData, creds); err != nil {
		return err
	}

	// Launch main containers concurrently
	g := errgroup.Group{}
	for _, container := range pod.Spec.Containers {
		if isVMContainer(container.Name, vmNames) {
			g.Go(c.createVMFunc(ctx, pod, container, extras.rootDir, volData, creds))
		} else {
			g.Go(c.createContainerFunc(ctx, pod, container, extras.rootDir, volData, creds))
		}
	}

	return g.Wait()
}

func (c *VzClientAPIs) runInitContainers(
	ctx context.Context, pod *corev1.Pod, extras *virtualizationGroupExtras,
	volData *volumes.PodVolumeData, creds resource.RegistryCredentialStore,
) error {
	ctx, cancel := context.WithTimeout(ctx, initTimeout(pod))
	defer cancel()

	for _, initCtr := range pod.Spec.InitContainers {
		state, err := c.runInitContainer(ctx, pod, initCtr, extras.rootDir, volData, creds)
		extras.initStates = append(extras.initStates, state)
		if err != nil {
			return fmt.Errorf("init container %s failed: %w", initCtr.Name, err)
		}
	}
	return nil
}

// initTimeout returns the deadline for init containers based on
// pod.Spec.ActiveDeadlineSeconds, falling back to DefaultInitTimeout.
func initTimeout(pod *corev1.Pod) time.Duration {
	if pod.Spec.ActiveDeadlineSeconds != nil {
		return time.Duration(*pod.Spec.ActiveDeadlineSeconds) * time.Second
	}
	return DefaultInitTimeout
}

func (c *VzClientAPIs) runInitContainer(
	ctx context.Context, pod *corev1.Pod, ctr corev1.Container,
	rootDir string, volData *volumes.PodVolumeData, creds resource.RegistryCredentialStore,
) (resource.Container, error) {
	if err := c.createNativeContainer(ctx, pod, ctr, rootDir, volData, creds); err != nil {
		return resource.Container{Name: ctr.Name, State: resource.ContainerState{Error: err.Error()}}, err
	}

	exitCode, err := c.ContainerClient.WaitForContainer(ctx, pod.Namespace, pod.Name, ctr.Name)
	if err != nil {
		return resource.Container{Name: ctr.Name, State: resource.ContainerState{Error: err.Error()}}, err
	}

	containers, err := c.ContainerClient.GetContainers(ctx, pod.Namespace, pod.Name)
	if err != nil {
		log.G(ctx).WithError(err).Warnf("failed to get init container state for %s", ctr.Name)
	}
	finalState := findContainer(ctr.Name, containers)

	if exitCode != 0 {
		return finalState, fmt.Errorf("exited with code %d", exitCode)
	}
	return finalState, nil
}

func findContainer(name string, containers []resource.Container) resource.Container {
	for _, c := range containers {
		if c.Name == name {
			return c
		}
	}
	return resource.Container{Name: name}
}

func (c *VzClientAPIs) createVMFunc(
	ctx context.Context, pod *corev1.Pod, ctr corev1.Container,
	rootDir string, volData *volumes.PodVolumeData, creds resource.RegistryCredentialStore,
) func() error {
	return func() error {
		return c.createVM(ctx, pod, ctr, rootDir, volData, creds)
	}
}

func (c *VzClientAPIs) createVM(
	ctx context.Context, pod *corev1.Pod, ctr corev1.Container,
	rootDir string, volData *volumes.PodVolumeData, creds resource.RegistryCredentialStore,
) error {
	vmCreds, _ := creds.ForImage(ctr.Image)
	vmParams, err := buildVMParams(ctx, pod, ctr, rootDir, volData, vmCreds)
	if err != nil {
		return err
	}
	return c.MacOSClient.CreateVirtualMachine(ctx, vmParams)
}

func buildVMParams(
	ctx context.Context, pod *corev1.Pod, ctr corev1.Container,
	rootDir string, volData *volumes.PodVolumeData, vmCreds resource.RegistryCredentials,
) (rm.VirtualMachineParams, error) {
	cpu, memorySize, err := validateVMResources(ctr)
	if err != nil {
		return rm.VirtualMachineParams{}, err
	}

	mounts, err := resolveMounts(ctx, pod, ctr, rootDir, volData)
	if err != nil {
		return rm.VirtualMachineParams{}, err
	}

	return rm.VirtualMachineParams{
		UID:              string(pod.UID),
		Image:            ctr.Image,
		Namespace:        pod.Namespace,
		Name:             pod.Name,
		ContainerName:    ctr.Name,
		CPU:              cpu,
		MemorySize:       memorySize,
		Mounts:           mounts,
		Env:              ctr.Env,
		PostStartAction:  extractPostStart(ctr),
		IgnoreImageCache: ctr.ImagePullPolicy == corev1.PullAlways,
		RegistryCreds:    vmCreds,
	}, nil
}

func validateVMResources(ctr corev1.Container) (uint, uint64, error) {
	rl := ctr.Resources.Requests
	cpu, err := utils.ExtractCPURequest(rl)
	if err != nil {
		return 0, 0, errdefs.AsInvalidInput(err)
	}
	if _, err = vm.ValidateCPUCount(cpu); err != nil {
		return 0, 0, errdefs.AsInvalidInput(err)
	}
	memorySize, err := utils.ExtractMemoryRequest(rl)
	if err != nil {
		return 0, 0, errdefs.AsInvalidInput(err)
	}
	if _, err = vm.ValidateMemorySize(memorySize); err != nil {
		return 0, 0, errdefs.AsInvalidInput(err)
	}
	return cpu, memorySize, nil
}

func (c *VzClientAPIs) createContainerFunc(
	ctx context.Context, pod *corev1.Pod, ctr corev1.Container,
	rootDir string, volData *volumes.PodVolumeData, creds resource.RegistryCredentialStore,
) func() error {
	return func() error {
		return c.createNativeContainer(ctx, pod, ctr, rootDir, volData, creds)
	}
}

func (c *VzClientAPIs) createNativeContainer(
	ctx context.Context, pod *corev1.Pod, ctr corev1.Container,
	rootDir string, volData *volumes.PodVolumeData, creds resource.RegistryCredentialStore,
) error {
	containerCreds, _ := creds.ForImage(ctr.Image)
	mounts, err := resolveMounts(ctx, pod, ctr, rootDir, volData)
	if err != nil {
		return err
	}

	networkName := c.podNetworkName(pod)
	dns := resolveDNS(pod, c.clusterDNS)

	return c.ContainerClient.CreateContainer(ctx, rm.ContainerParams{
		PodNamespace:    pod.Namespace,
		PodName:         pod.Name,
		Name:            ctr.Name,
		Image:           ctr.Image,
		ImagePullPolicy: ctr.ImagePullPolicy,
		Mounts:          mounts,
		Env:             ctr.Env,
		Command:         ctr.Command,
		Args:            ctr.Args,
		WorkingDir:      ctr.WorkingDir,
		TTY:             ctr.TTY,
		Stdin:           ctr.Stdin,
		StdinOnce:       ctr.StdinOnce,
		PostStartAction: extractPostStart(ctr),
		RegistryCreds:   containerCreds,
		SecurityContext: ctr.SecurityContext,
		Resources:       ctr.Resources,
		Network:         networkName,
		DNS:             dns,
	})
}

// podNetworkName returns the network name for a pod from its extras, if set.
func (c *VzClientAPIs) podNetworkName(pod *corev1.Pod) string {
	key := types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}
	val, ok := c.extras.Load(key)
	if !ok {
		return ""
	}
	extras, ok := val.(*virtualizationGroupExtras)
	if !ok {
		return ""
	}
	return extras.networkName
}

// resolveDNS determines DNS servers based on pod spec and cluster configuration.
func resolveDNS(pod *corev1.Pod, clusterDNS string) []string {
	if hasPodDNSOverride(pod) {
		return pod.Spec.DNSConfig.Nameservers
	}
	if pod.Spec.DNSPolicy == corev1.DNSDefault {
		return nil
	}
	// ClusterFirst (default) or ClusterFirstWithHostNet
	if clusterDNS != "" {
		return []string{clusterDNS}
	}
	return nil
}

func hasPodDNSOverride(pod *corev1.Pod) bool {
	return pod.Spec.DNSPolicy == corev1.DNSNone &&
		pod.Spec.DNSConfig != nil &&
		len(pod.Spec.DNSConfig.Nameservers) > 0
}

func resolveMounts(
	ctx context.Context, pod *corev1.Pod, ctr corev1.Container,
	rootDir string, volData *volumes.PodVolumeData,
) ([]volumes.Mount, error) {
	return volumes.CreateContainerMounts(ctx, volumes.VolumeContext{
		PodVolRoot:          rootDir,
		Pod:                 pod,
		Container:           ctr,
		ServiceAccountToken: volData.ServiceAccountToken,
		ConfigMaps:          volData.ConfigMaps,
		Secrets:             volData.Secrets,
		PVCs:                volData.PVCs,
	})
}

func extractPostStart(ctr corev1.Container) *resource.ExecAction {
	lc := ctr.Lifecycle
	if lc == nil || lc.PostStart == nil || lc.PostStart.Exec == nil {
		return nil
	}
	return &resource.ExecAction{
		Command:         lc.PostStart.Exec.Command,
		TimeoutDuration: PostStartCommandTimeout,
	}
}

// DeleteVirtualizationGroup deletes an existing virtualization group specified by namespace and name.
func (c *VzClientAPIs) DeleteVirtualizationGroup(ctx context.Context, namespace, name string, gracePeriod int64) (err error) {
	ctx, span := trace.StartSpan(ctx, "VZClient.DeleteVirtualizationGroup")
	defer func() {
		span.SetStatus(err)
		span.End()
	}()

	key := types.NamespacedName{Namespace: namespace, Name: name}
	extrasValue, loaded := c.extras.Load(key)
	if !loaded {
		return errVirtualizationGroupNotFound
	}

	extras, ok := extrasValue.(*virtualizationGroupExtras)
	if !ok {
		return errVirtualizationGroupNotFound
	}

	// Initiate the deletion process only once
	extras.deleteOnce.Do(func() {
		defer func() {
			c.extras.Delete(key)
			close(extras.deleteDone)

			// Clean up the pod volume root directory and cancel the context
			if extras.cancelFunc != nil {
				// Group context must be cancelled after cleaning up all resources
				// related to Virtual Machine and Containers.
				extras.cancelFunc()
			}

			if extras.rootDir != "" {
				if err := os.RemoveAll(extras.rootDir); err != nil {
					log.G(ctx).WithError(err).Warn("Failed to clean up pod volume root")
				}
			}
		}()

		var wg sync.WaitGroup
		var vmErr, containerErr error

		// Delete virtual machine
		wg.Add(1)
		go func() {
			defer wg.Done()
			vmErr = c.MacOSClient.DeleteVirtualMachine(ctx, namespace, name, gracePeriod)
		}()

		// Delete containers
		if c.ContainerClient != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				containerErr = c.ContainerClient.RemoveContainers(ctx, namespace, name, gracePeriod)
			}()
		}

		wg.Wait() // Wait for both operations to complete

		// Clean up the pod network after containers are removed
		if extras.networkName != "" && c.ContainerClient != nil {
			if netErr := c.ContainerClient.DeletePodNetwork(ctx, extras.networkName); netErr != nil {
				log.G(ctx).WithError(netErr).Warn("Failed to delete pod network")
			}
		}

		switch {
		case vmErr != nil && containerErr != nil:
			if errdefs.IsNotFound(vmErr) && errdefs.IsNotFound(containerErr) {
				extras.deleteDone <- errVirtualizationGroupNotFound
			} else {
				extras.deleteDone <- errors.Join(vmErr, containerErr)
			}
		case vmErr != nil:
			if errdefs.IsNotFound(vmErr) {
				extras.deleteDone <- nil
			} else {
				extras.deleteDone <- vmErr
			}
		case containerErr != nil:
			if errdefs.IsNotFound(containerErr) {
				extras.deleteDone <- nil
			} else {
				extras.deleteDone <- containerErr
			}
		default:
			extras.deleteDone <- nil
		}
	})

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err = <-extras.deleteDone:
		return err
	}
}

// GetVirtualizationGroup retrieves the details of a specified virtualization group.
func (c *VzClientAPIs) GetVirtualizationGroup(ctx context.Context, namespace, name string) (vg *VirtualizationGroup, err error) {
	ctx, span := trace.StartSpan(ctx, "VZClient.GetVirtualizationGroup")
	defer func() {
		span.SetStatus(err)
		span.End()
	}()

	var containers []resource.Container
	var vmResult resource.VirtualMachine
	var containerErr, vmErr error

	// Fetch containers
	if c.ContainerClient != nil {
		containers, containerErr = c.ContainerClient.GetContainers(ctx, namespace, name)
		if containerErr != nil && !errdefs.IsNotFound(containerErr) {
			err = containerErr
		}
	} else {
		containerErr = errdefs.NotFound("container client not available")
	}

	// Fetch virtual machine
	vm, vmErr := c.MacOSClient.GetVirtualMachine(ctx, namespace, name)
	if vmErr == nil {
		vmResult = &vm
	} else if !errdefs.IsNotFound(vmErr) {
		err = errors.Join(err, vmErr)
	}

	// If both clients return not found errors, return a combined not found error
	if errdefs.IsNotFound(containerErr) && errdefs.IsNotFound(vmErr) {
		return nil, errVirtualizationGroupNotFound
	}

	return &VirtualizationGroup{
		InitContainers:      c.loadInitStates(namespace, name),
		Containers:          containers,
		MacOSVirtualMachine: vmResult,
	}, err
}

func (c *VzClientAPIs) loadInitStates(namespace, name string) []resource.Container {
	key := types.NamespacedName{Namespace: namespace, Name: name}
	extrasVal, ok := c.extras.Load(key)
	if !ok {
		return []resource.Container{}
	}
	extras, ok := extrasVal.(*virtualizationGroupExtras)
	if !ok {
		return []resource.Container{}
	}
	return extras.initStates
}

// GetVirtualizationGroupListResult retrieves a list of all virtualization groups.
func (c *VzClientAPIs) GetVirtualizationGroupListResult(ctx context.Context) (l map[types.NamespacedName]*VirtualizationGroup, err error) {
	ctx, span := trace.StartSpan(ctx, "VZClient.GetVirtualizationGroupListResult")
	defer func() {
		span.SetStatus(err)
		span.End()
	}()
	logger := log.G(ctx)

	var wg sync.WaitGroup
	var vmErr, containerErr error
	var vms map[types.NamespacedName]resource.MacOSVirtualMachine
	var containers map[types.NamespacedName][]resource.Container

	// Fetch virtual machines
	wg.Add(1)
	go func() {
		defer wg.Done()
		vms, vmErr = c.MacOSClient.GetVirtualMachineListResult(ctx)
		if vmErr != nil {
			logger.WithError(vmErr).Warn("Error getting VM list")
		}
	}()

	// Fetch containers
	if c.ContainerClient != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			containers, containerErr = c.ContainerClient.GetContainersListResult(ctx)
			if containerErr != nil {
				logger.WithError(containerErr).Warn("Error getting container list")
			}
		}()
	}

	wg.Wait()

	// Combine errors if both exist
	if vmErr != nil && containerErr != nil {
		err = errors.Join(vmErr, containerErr)
	} else if vmErr != nil {
		err = vmErr
	} else if containerErr != nil {
		err = containerErr
	}

	// Initialize the result map
	l = make(map[types.NamespacedName]*VirtualizationGroup)

	// Combine the results
	for k, v := range vms {
		vmCopy := v // capture loop variable
		l[k] = &VirtualizationGroup{
			MacOSVirtualMachine: &vmCopy,
		}
	}

	for k, c := range containers {
		if vg, exists := l[k]; exists {
			vg.Containers = c
		} else {
			l[k] = &VirtualizationGroup{
				Containers: c,
			}
		}
	}

	return l, err
}

// GetContainerLogs retrieves the logs of a specified container in the virtualization group.
func (c *VzClientAPIs) GetContainerLogs(ctx context.Context, namespace, podName, containerName string, opts api.ContainerLogOpts) (in io.ReadCloser, err error) {
	ctx, span := trace.StartSpan(ctx, "VZClient.GetContainerLogs")
	defer func() {
		span.SetStatus(err)
		span.End()
	}()

	if c.ContainerClient != nil && c.ContainerClient.IsContainerPresent(ctx, namespace, podName, containerName) {
		return c.ContainerClient.GetContainerLogs(ctx, namespace, podName, containerName, opts)
	}

	return nil, errdefs.InvalidInput("container logs are not supported for macOS virtual machines")
}

// ExecuteContainerCommand executes a command inside a specified container.
func (c *VzClientAPIs) ExecuteContainerCommand(ctx context.Context, namespace, podName, containerName string, cmd []string, attach api.AttachIO) (err error) {
	ctx, span := trace.StartSpan(ctx, "VZClient.ExecuteContainerCommand")
	defer func() {
		span.SetStatus(err)
		span.End()
	}()

	if c.ContainerClient != nil && c.ContainerClient.IsContainerPresent(ctx, namespace, podName, containerName) {
		return c.ContainerClient.ExecInContainer(ctx, namespace, podName, containerName, cmd, attach)
	}

	return c.MacOSClient.ExecInVirtualMachine(ctx, namespace, podName, cmd, attach)
}

// AttachToContainer attaches to a specified container.
func (c *VzClientAPIs) AttachToContainer(ctx context.Context, namespace, podName, containerName string, attach api.AttachIO) (err error) {
	ctx, span := trace.StartSpan(ctx, "VZClient.AttachToContainer")
	defer func() {
		span.SetStatus(err)
		span.End()
	}()

	if c.ContainerClient != nil && c.ContainerClient.IsContainerPresent(ctx, namespace, podName, containerName) {
		return c.ContainerClient.AttachToContainer(ctx, namespace, podName, containerName, attach)
	}

	return c.MacOSClient.ExecInVirtualMachine(ctx, namespace, podName, nil, attach)
}

func (c *VzClientAPIs) GetVirtualizationGroupStats(ctx context.Context, pod *corev1.Pod) (cs []stats.ContainerStats, err error) {
	ctx, span := trace.StartSpan(ctx, "VZClient.GetVirtualizationGroupStats")
	defer func() {
		span.SetStatus(err)
		span.End()
	}()

	vmNames := VMContainerNames(pod)
	for _, ctr := range pod.Spec.Containers {
		s, err := c.containerStats(ctx, pod.Namespace, pod.Name, ctr.Name, vmNames)
		if err != nil {
			return nil, err
		}
		cs = append(cs, s)
	}
	return cs, nil
}

func (c *VzClientAPIs) containerStats(ctx context.Context, ns, pod, ctrName string, vmNames map[string]bool) (stats.ContainerStats, error) {
	if !isVMContainer(ctrName, vmNames) {
		if c.ContainerClient == nil {
			return stats.ContainerStats{}, fmt.Errorf("no container client for native container %q", ctrName)
		}
		return c.ContainerClient.GetContainerStats(ctx, ns, pod, ctrName)
	}
	s, err := c.MacOSClient.GetVirtualMachineStats(ctx, ns, pod)
	if err != nil {
		return stats.ContainerStats{}, err
	}
	s.Name = ctrName
	return s, nil
}

// getPodVolumeRoot returns the root path for the volumes of a pod
func (c *VzClientAPIs) getPodVolumeRoot(pod *corev1.Pod) string {
	return filepath.Join(c.cachePath, PodMountsDir, string(pod.UID))
}
