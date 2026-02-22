package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/agoda-com/macOS-vz-kubelet/internal/utils"
	"github.com/agoda-com/macOS-vz-kubelet/pkg/client"
	"github.com/agoda-com/macOS-vz-kubelet/pkg/resource"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// virtualizationGroupToPod converts a VirtualizationGroup to a Kubernetes Pod status.
func (p *MacOSVZProvider) virtualizationGroupToPod(ctx context.Context, vg *client.VirtualizationGroup, namespace, name string) (*corev1.Pod, error) {
	pod, err := p.podLister.Pods(namespace).Get(name)
	if err != nil {
		return nil, err
	}

	podState := p.buildPodStatus(ctx, vg, pod)
	updatedPod := pod.DeepCopy()
	updatedPod.Status = *podState

	return updatedPod, nil
}

// buildPodStatus constructs the pod's status from the provided virtualization group.
func (p *MacOSVZProvider) buildPodStatus(_ context.Context, vg *client.VirtualizationGroup, pod *corev1.Pod) *corev1.PodStatus {
	var times statusTimes
	vmNames := client.VMContainerNames(pod)
	containerStatuses := buildContainerStatuses(vg, pod, vmNames, &times)

	return &corev1.PodStatus{
		Phase:                  getPodPhaseFromVirtualizationGroup(vg),
		Conditions:             getPodConditionsFromVirtualizationGroup(vg, pod, times.firstStart, times.lastUpdate),
		HostIP:                 p.nodeIPAddress,
		PodIP:                  podIPAddress(vg),
		StartTime:              times.startTime(),
		InitContainerStatuses:  buildInitContainerStatuses(vg, pod, &times),
		ContainerStatuses:      containerStatuses,
	}
}

type statusTimes struct {
	firstStart time.Time
	lastUpdate time.Time
}

func (t *statusTimes) startTime() *metav1.Time {
	if t.firstStart.IsZero() {
		return nil
	}
	return &metav1.Time{Time: t.firstStart}
}

func (t *statusTimes) trackVM(vm resource.VirtualMachine) {
	if startedAt := vm.StartedAt(); startedAt != nil {
		t.trackStart(*startedAt)
	}
	if finishedAt := vm.FinishedAt(); finishedAt != nil {
		t.trackFinish(*finishedAt)
	}
}

func (t *statusTimes) trackContainer(ctr resource.Container) {
	if !ctr.State.StartedAt.IsZero() {
		t.trackStart(ctr.State.StartedAt)
	}
	if !ctr.State.FinishedAt.IsZero() {
		t.trackFinish(ctr.State.FinishedAt)
	}
}

func (t *statusTimes) trackStart(ts time.Time) {
	if t.firstStart.IsZero() || ts.Before(t.firstStart) {
		t.firstStart = ts
	}
	if ts.After(t.lastUpdate) {
		t.lastUpdate = ts
	}
}

func (t *statusTimes) trackFinish(ts time.Time) {
	if ts.After(t.lastUpdate) {
		t.lastUpdate = ts
	}
}

func buildContainerStatuses(vg *client.VirtualizationGroup, pod *corev1.Pod, vmNames map[string]bool, times *statusTimes) []corev1.ContainerStatus {
	statuses := make([]corev1.ContainerStatus, 0, len(pod.Spec.Containers))
	for _, c := range pod.Spec.Containers {
		if s, ok := containerStatusFor(vg, c, vmNames, pod.CreationTimestamp.Time, times); ok {
			statuses = append(statuses, s)
		}
	}
	return statuses
}

func containerStatusFor(
	vg *client.VirtualizationGroup, spec corev1.Container,
	vmNames map[string]bool, createdAt time.Time, times *statusTimes,
) (corev1.ContainerStatus, bool) {
	if vmNames[spec.Name] {
		if !vg.HasVM() {
			return corev1.ContainerStatus{}, false
		}
		return buildVMStatus(vg.MacOSVirtualMachine, spec, createdAt, times), true
	}
	ctr, err := getContainerWithName(spec.Name, vg.Containers)
	if err != nil {
		return corev1.ContainerStatus{}, false
	}
	return buildContainerStatus(ctr, spec, createdAt, times), true
}

func buildVMStatus(vm resource.VirtualMachine, spec corev1.Container, createdAt time.Time, times *statusTimes) corev1.ContainerStatus {
	times.trackVM(vm)
	vmState := vm.State()
	podIP := vm.IPAddress()
	started := podIP != ""
	ready := vmState == resource.VirtualMachineStateRunning
	return corev1.ContainerStatus{
		Name:        spec.Name,
		State:       vmToContainerState(vm, createdAt),
		Ready:       ready,
		Started:     &started,
		Image:       spec.Image,
		ContainerID: utils.GetContainerID(resource.MacOSRuntime, spec.Name),
	}
}

func buildContainerStatus(ctr resource.Container, spec corev1.Container, createdAt time.Time, times *statusTimes) corev1.ContainerStatus {
	times.trackContainer(ctr)
	s := ctr.State.Status
	started := s == resource.ContainerStatusRunning
	return corev1.ContainerStatus{
		Name:        spec.Name,
		State:       containerToContainerState(ctr, createdAt),
		Ready:       s == resource.ContainerStatusRunning,
		Started:     &started,
		Image:       spec.Image,
		ContainerID: utils.GetContainerID(resource.ContainerRuntime, spec.Name),
	}
}

func buildInitContainerStatuses(vg *client.VirtualizationGroup, pod *corev1.Pod, times *statusTimes) []corev1.ContainerStatus {
	if len(pod.Spec.InitContainers) == 0 {
		return nil
	}
	statuses := make([]corev1.ContainerStatus, 0, len(pod.Spec.InitContainers))
	for _, spec := range pod.Spec.InitContainers {
		ctr, err := getContainerWithName(spec.Name, vg.InitContainers)
		if err != nil {
			continue
		}
		statuses = append(statuses, buildContainerStatus(ctr, spec, pod.CreationTimestamp.Time, times))
	}
	return statuses
}

func allInitContainersDone(vg *client.VirtualizationGroup, pod *corev1.Pod) bool {
	if len(pod.Spec.InitContainers) == 0 {
		return true
	}
	for _, spec := range pod.Spec.InitContainers {
		ctr, err := getContainerWithName(spec.Name, vg.InitContainers)
		if err != nil {
			return false
		}
		if ctr.State.ExitCode != 0 || !isTerminal(ctr.State.Status) {
			return false
		}
	}
	return true
}

func isTerminal(s resource.ContainerStatus) bool {
	return s.IsTerminal()
}

// podIPAddress returns the pod's IP from the VM, or empty if no VM exists.
func podIPAddress(vg *client.VirtualizationGroup) string {
	if !vg.HasVM() {
		return ""
	}
	return vg.MacOSVirtualMachine.IPAddress()
}

// vmToContainerState converts the macOS VM state to a Kubernetes container state.
func vmToContainerState(vm resource.VirtualMachine, podCreationTime time.Time) corev1.ContainerState {
	startTime := podCreationTime
	finishTime := podCreationTime
	if startedAt := vm.StartedAt(); startedAt != nil {
		startTime = *startedAt
	}
	if finishedAt := vm.FinishedAt(); finishedAt != nil {
		finishTime = *finishedAt
	}

	switch vm.State() {
	case resource.VirtualMachineStatePreparing:
		return corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{
				Reason:  "Downloading",
				Message: "VM is downloading image from the registry",
			},
		}
	case resource.VirtualMachineStateStarting:
		return corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{
				Reason:  "Starting",
				Message: "VM is starting",
			},
		}
	case resource.VirtualMachineStateRunning:
		return corev1.ContainerState{
			Running: &corev1.ContainerStateRunning{
				StartedAt: metav1.NewTime(startTime),
			},
		}
	case resource.VirtualMachineStateTerminating, resource.VirtualMachineStateTerminated:
		return corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{
				ExitCode:   0,
				Reason:     "Completed",
				Message:    "VM is stopped",
				StartedAt:  metav1.NewTime(startTime),
				FinishedAt: metav1.NewTime(finishTime),
			},
		}
	case resource.VirtualMachineStateFailed:
		return corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{
				ExitCode:   1,
				Reason:     "Error",
				Message:    fmt.Sprintf("VM has failed: %v", vm.Error()),
				StartedAt:  metav1.NewTime(startTime),
				FinishedAt: metav1.NewTime(finishTime),
			},
		}
	}

	return corev1.ContainerState{}
}

// containerToContainerState converts the container state to a Kubernetes container state.
func containerToContainerState(container resource.Container, podCreationTime time.Time) corev1.ContainerState {
	startTime := podCreationTime
	finishTime := podCreationTime
	if !container.State.StartedAt.IsZero() {
		startTime = container.State.StartedAt
	}
	if !container.State.FinishedAt.IsZero() {
		finishTime = container.State.FinishedAt
	}

	switch container.State.Status {
	case resource.ContainerStatusWaiting:
		if container.State.Error != "" {
			// mimic standard kubernetes behavior for
			// container error during pre-running stage
			return corev1.ContainerState{
				Waiting: &corev1.ContainerStateWaiting{
					Reason:  "Error",
					Message: container.State.Error,
				},
			}
		}
		return corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{
				Reason: "ContainerCreating",
			},
		}
	case resource.ContainerStatusCreated:
		return corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{
				Reason:  "ContainerCreated",
				Message: "Container has been created",
			},
		}
	case resource.ContainerStatusRunning:
		return corev1.ContainerState{
			Running: &corev1.ContainerStateRunning{
				StartedAt: metav1.NewTime(startTime),
			},
		}
	case resource.ContainerStatusPaused:
		return corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{
				Reason:  "ContainerPaused",
				Message: "Container is paused",
			},
		}
	case resource.ContainerStatusRestarting:
		return corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{
				Reason:  "ContainerRestarting",
				Message: "Container is restarting",
			},
		}
	case resource.ContainerStatusOOMKilled:
		return corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{
				ExitCode:   137,
				Reason:     "OOMKilled",
				Message:    "Container was killed due to out of memory",
				StartedAt:  metav1.NewTime(startTime),
				FinishedAt: metav1.NewTime(finishTime),
			},
		}
	case resource.ContainerStatusDead:
		return corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{
				ExitCode:   int32(container.State.ExitCode),
				Reason:     "ContainerDead",
				Message:    container.State.Error,
				StartedAt:  metav1.NewTime(startTime),
				FinishedAt: metav1.NewTime(finishTime),
			},
		}
	default:
		return corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{
				ExitCode:   int32(container.State.ExitCode),
				Reason:     "Unknown",
				Message:    container.State.Error,
				StartedAt:  metav1.NewTime(startTime),
				FinishedAt: metav1.NewTime(finishTime),
			},
		}
	}
}

// getContainerWithName finds and returns a container with the specified name from a list of containers.
func getContainerWithName(name string, list []resource.Container) (resource.Container, error) {
	for _, c := range list {
		if c.Name == name {
			return c, nil
		}
	}

	return resource.Container{}, fmt.Errorf("container %s not found", name)
}

// getPodPhaseFromVirtualizationGroup determines the pod phase based on the state of the virtualization group.
func getPodPhaseFromVirtualizationGroup(vg *client.VirtualizationGroup) corev1.PodPhase {
	if vg.HasVM() {
		return podPhaseWithVM(vg)
	}
	return podPhaseContainersOnly(vg.Containers)
}

func podPhaseWithVM(vg *client.VirtualizationGroup) corev1.PodPhase {
	switch vg.MacOSVirtualMachine.State() {
	case resource.VirtualMachineStatePreparing, resource.VirtualMachineStateStarting:
		return corev1.PodPending
	case resource.VirtualMachineStateTerminated:
		return corev1.PodSucceeded
	case resource.VirtualMachineStateFailed:
		return corev1.PodFailed
	}

	// VM is running/terminating — also check containers
	if len(vg.Containers) == 0 {
		if vg.MacOSVirtualMachine.IPAddress() == "" {
			return corev1.PodPending
		}
		return corev1.PodRunning
	}
	return containerPhase(vg.Containers, vg.MacOSVirtualMachine.IPAddress() != "")
}

func podPhaseContainersOnly(containers []resource.Container) corev1.PodPhase {
	return containerPhase(containers, true)
}

func containerPhase(containers []resource.Container, networkReady bool) corev1.PodPhase {
	allRunning := true
	for _, ctr := range containers {
		switch ctr.State.Status {
		case resource.ContainerStatusWaiting, resource.ContainerStatusCreated:
			return corev1.PodPending
		case resource.ContainerStatusRunning:
			continue
		case resource.ContainerStatusOOMKilled, resource.ContainerStatusUnknown:
			return corev1.PodFailed
		default:
			allRunning = false
		}
	}
	if !networkReady {
		return corev1.PodPending
	}
	if allRunning {
		return corev1.PodRunning
	}
	return corev1.PodUnknown
}

// getPodConditionsFromVirtualizationGroup determines the pod conditions based on the state of the virtualization group.
func getPodConditionsFromVirtualizationGroup(vg *client.VirtualizationGroup, pod *corev1.Pod, firstStart, lastUpdate time.Time) []corev1.PodCondition {
	initialized, ready := evaluateConditions(vg)
	if !allInitContainersDone(vg, pod) {
		initialized = corev1.ConditionFalse
	}
	podCreationTime := pod.CreationTimestamp.Time

	return []corev1.PodCondition{
		{
			Type:               corev1.PodScheduled,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.Time{Time: podCreationTime},
		},
		{
			Type:               corev1.PodInitialized,
			Status:             initialized,
			LastTransitionTime: metav1.Time{Time: firstStart},
		},
		{
			Type:               corev1.PodReady,
			Status:             ready,
			LastTransitionTime: metav1.Time{Time: lastUpdate},
		},
	}
}

func evaluateConditions(vg *client.VirtualizationGroup) (corev1.ConditionStatus, corev1.ConditionStatus) {
	if !vg.HasVM() {
		return containerConditions(vg.Containers)
	}
	return vmConditions(vg)
}

func vmConditions(vg *client.VirtualizationGroup) (corev1.ConditionStatus, corev1.ConditionStatus) {
	switch vg.MacOSVirtualMachine.State() {
	case resource.VirtualMachineStatePreparing, resource.VirtualMachineStateStarting:
		return corev1.ConditionFalse, corev1.ConditionFalse
	case resource.VirtualMachineStateRunning, resource.VirtualMachineStateTerminating:
		return vmRunningConditions(vg.Containers)
	case resource.VirtualMachineStateTerminated:
		return corev1.ConditionTrue, corev1.ConditionFalse
	case resource.VirtualMachineStateFailed:
		return corev1.ConditionFalse, corev1.ConditionFalse
	default:
		return corev1.ConditionFalse, corev1.ConditionFalse
	}
}

// vmRunningConditions evaluates conditions when the VM is running/terminating.
// Containers still starting (waiting/created) are tolerated; fatal states fail immediately.
func vmRunningConditions(containers []resource.Container) (corev1.ConditionStatus, corev1.ConditionStatus) {
	allRunning := true
	for _, ctr := range containers {
		switch ctr.State.Status {
		case resource.ContainerStatusRunning:
			continue
		case resource.ContainerStatusOOMKilled, resource.ContainerStatusUnknown:
			return corev1.ConditionFalse, corev1.ConditionFalse
		case resource.ContainerStatusWaiting, resource.ContainerStatusCreated:
			// Container still starting — pod is initialized but not ready
			allRunning = false
		default:
			allRunning = false
		}
	}
	if allRunning {
		return corev1.ConditionTrue, corev1.ConditionTrue
	}
	return corev1.ConditionTrue, corev1.ConditionFalse
}

func containerConditions(containers []resource.Container) (corev1.ConditionStatus, corev1.ConditionStatus) {
	if !allContainersHealthy(containers) {
		return corev1.ConditionFalse, corev1.ConditionFalse
	}
	return corev1.ConditionTrue, corev1.ConditionTrue
}

func allContainersHealthy(containers []resource.Container) bool {
	for _, ctr := range containers {
		switch ctr.State.Status {
		case resource.ContainerStatusRunning:
			continue
		default:
			return false
		}
	}
	return true
}
