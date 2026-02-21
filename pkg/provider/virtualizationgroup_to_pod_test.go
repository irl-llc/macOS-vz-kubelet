package provider_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"gotest.tools/v3/golden"

	"github.com/agoda-com/macOS-vz-kubelet/pkg/client"
	clientmocks "github.com/agoda-com/macOS-vz-kubelet/pkg/client/mocks"
	"github.com/agoda-com/macOS-vz-kubelet/pkg/provider"
	"github.com/agoda-com/macOS-vz-kubelet/pkg/resource"
	vmmocks "github.com/agoda-com/macOS-vz-kubelet/pkg/resource/mocks"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/yaml"
)

func TestGetPodStatus(t *testing.T) {
	// Containers
	oneContainer := []corev1.Container{
		{Name: "container-0", Image: "localhost:5000/macos:latest"},
	}
	twoContainers := []corev1.Container{
		{Name: "container-0", Image: "localhost:5000/macos:latest"},
		{Name: "container-1", Image: "localhost:5000/sidecar:1.27.1"},
	}

	fakeTime := time.Date(2012, 12, 12, 12, 12, 12, 0, time.UTC)

	tests := []struct {
		name              string
		containers        []corev1.Container
		vmState           resource.VirtualMachineState
		vmIP              string
		vmStartedAt       time.Time
		vmFinishedAt      time.Time
		vmError           error
		containerStates   []resource.ContainerState
		expectForceDelete bool
	}{
		{
			name:       "VM preparing/no containers",
			containers: oneContainer,
			vmState:    resource.VirtualMachineStatePreparing,
		},
		{
			name:       "VM preparing/container running",
			containers: twoContainers,
			vmState:    resource.VirtualMachineStatePreparing,
			containerStates: []resource.ContainerState{
				{Status: resource.ContainerStatusRunning, StartedAt: fakeTime},
			},
		},
		{
			name:       "VM starting/no containers",
			containers: oneContainer,
			vmState:    resource.VirtualMachineStateStarting,
		},
		{
			name:       "VM starting/container running",
			containers: twoContainers,
			vmState:    resource.VirtualMachineStateStarting,
			containerStates: []resource.ContainerState{
				{Status: resource.ContainerStatusRunning, StartedAt: fakeTime},
			},
		},
		{
			name:        "VM running/no ip and no containers",
			containers:  oneContainer,
			vmState:     resource.VirtualMachineStateRunning,
			vmStartedAt: fakeTime,
		},
		{
			name:        "VM running/no containers",
			containers:  oneContainer,
			vmState:     resource.VirtualMachineStateRunning,
			vmIP:        "10.0.0.3",
			vmStartedAt: fakeTime,
		},
		{
			name:        "VM running/no ip and container running",
			containers:  twoContainers,
			vmState:     resource.VirtualMachineStateRunning,
			vmStartedAt: fakeTime,
			containerStates: []resource.ContainerState{
				{Status: resource.ContainerStatusRunning, StartedAt: fakeTime.Add(-time.Minute)},
			},
		},
		{
			name:        "VM running/container waiting",
			containers:  twoContainers,
			vmState:     resource.VirtualMachineStateRunning,
			vmIP:        "10.0.0.3",
			vmStartedAt: fakeTime,
			containerStates: []resource.ContainerState{
				{Status: resource.ContainerStatusWaiting},
			},
		},
		{
			name:        "VM running/container waiting with error",
			containers:  twoContainers,
			vmState:     resource.VirtualMachineStateRunning,
			vmIP:        "10.0.0.3",
			vmStartedAt: fakeTime,
			containerStates: []resource.ContainerState{
				{Status: resource.ContainerStatusWaiting, Error: assert.AnError.Error()},
			},
		},
		{
			name:        "VM running/container created",
			containers:  twoContainers,
			vmState:     resource.VirtualMachineStateRunning,
			vmIP:        "10.0.0.3",
			vmStartedAt: fakeTime,
			containerStates: []resource.ContainerState{
				{Status: resource.ContainerStatusCreated},
			},
		},
		{
			name:        "VM running/container running",
			containers:  twoContainers,
			vmState:     resource.VirtualMachineStateRunning,
			vmIP:        "10.0.0.3",
			vmStartedAt: fakeTime,
			containerStates: []resource.ContainerState{
				{Status: resource.ContainerStatusRunning, StartedAt: fakeTime},
			},
		},
		{
			name:            "VM running/container missing in VG",
			containers:      twoContainers,
			vmState:         resource.VirtualMachineStateRunning,
			vmIP:            "10.0.0.3",
			vmStartedAt:     fakeTime,
			containerStates: nil, // missing report from virtualization group
		},
		{
			name:        "VM running/container paused",
			containers:  twoContainers,
			vmState:     resource.VirtualMachineStateRunning,
			vmIP:        "10.0.0.3",
			vmStartedAt: fakeTime,
			containerStates: []resource.ContainerState{
				{Status: resource.ContainerStatusPaused},
			},
		},
		{
			name:        "VM running/container restarting",
			containers:  twoContainers,
			vmState:     resource.VirtualMachineStateRunning,
			vmIP:        "10.0.0.3",
			vmStartedAt: fakeTime,
			containerStates: []resource.ContainerState{
				{Status: resource.ContainerStatusRestarting},
			},
		},
		{
			name:        "VM running/container OOMKilled",
			containers:  twoContainers,
			vmState:     resource.VirtualMachineStateRunning,
			vmIP:        "10.0.0.3",
			vmStartedAt: fakeTime,
			containerStates: []resource.ContainerState{
				{Status: resource.ContainerStatusOOMKilled, StartedAt: fakeTime.Add(-time.Minute), FinishedAt: fakeTime},
			},
			expectForceDelete: true,
		},
		{
			name:        "VM running/container dead",
			containers:  twoContainers,
			vmState:     resource.VirtualMachineStateRunning,
			vmIP:        "10.0.0.3",
			vmStartedAt: fakeTime,
			containerStates: []resource.ContainerState{
				{Status: resource.ContainerStatusDead, StartedAt: fakeTime.Add(-time.Minute), FinishedAt: fakeTime.Add(time.Minute), ExitCode: 2},
			},
		},
		{
			name:        "VM running/container lost",
			containers:  twoContainers,
			vmState:     resource.VirtualMachineStateRunning,
			vmIP:        "10.0.0.3",
			vmStartedAt: fakeTime,
			containerStates: []resource.ContainerState{
				{Status: resource.ContainerStatusUnknown, StartedAt: fakeTime.Add(-time.Minute), FinishedAt: fakeTime, ExitCode: 9},
			},
			expectForceDelete: true,
		},
		{
			name:        "VM terminating/no containers",
			containers:  oneContainer,
			vmState:     resource.VirtualMachineStateTerminating,
			vmIP:        "10.0.0.3",
			vmStartedAt: fakeTime,
		},
		{
			name:              "VM terminated/no containers",
			containers:        oneContainer,
			vmState:           resource.VirtualMachineStateTerminated,
			vmIP:              "10.0.0.3",
			vmStartedAt:       fakeTime,
			vmFinishedAt:      fakeTime.Add(time.Minute),
			expectForceDelete: true,
		},
		{
			name:              "VM failed/no containers",
			containers:        oneContainer,
			vmState:           resource.VirtualMachineStateFailed,
			vmIP:              "10.0.0.3",
			vmStartedAt:       fakeTime,
			vmFinishedAt:      fakeTime.Add(time.Minute),
			vmError:           assert.AnError,
			expectForceDelete: true,
		},
		{
			name:         "VM lost/no containers",
			containers:   oneContainer,
			vmState:      122, // random state that doesn't exist
			vmIP:         "10.0.0.3",
			vmStartedAt:  fakeTime,
			vmFinishedAt: fakeTime.Add(time.Minute),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()

			// Create virtualization group with VM and containers
			vm := vmmocks.NewMockVirtualMachine(t)
			vm.On("State").Return(tc.vmState, nil)
			vm.On("IPAddress").Return(tc.vmIP, nil)
			var startedAt, finishedAt *time.Time
			if !tc.vmStartedAt.IsZero() {
				startedAt = &tc.vmStartedAt
			}
			if !tc.vmFinishedAt.IsZero() {
				finishedAt = &tc.vmFinishedAt
			}
			vm.On("StartedAt").Return(startedAt)
			vm.On("FinishedAt").Return(finishedAt)
			if tc.vmError != nil {
				vm.On("Error").Return(tc.vmError)
			}

			containers := make([]resource.Container, len(tc.containerStates))
			for i, state := range tc.containerStates {
				containers[i] = resource.Container{
					Name:  fmt.Sprintf("container-%d", i+1),
					State: state,
				}
			}

			vg := &client.VirtualizationGroup{
				MacOSVirtualMachine: vm,
				Containers:          containers,
			}

			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					Containers: tc.containers,
				},
			}

			// Mock the virtualization client
			vzClient := clientmocks.NewMockVzClientInterface(t)
			vzClient.On("GetVirtualizationGroup", mock.Anything, pod.Namespace, pod.Name).Return(vg, nil).Once()
			if tc.expectForceDelete {
				vzClient.On("DeleteVirtualizationGroup", mock.Anything, pod.Namespace, pod.Name, provider.DefaultDeleteVZGroupGracePeriodSeconds).Return(nil).Once()
			}

			// Set up provider
			p := setupVZProviderWithPodInformer(t, ctx, vzClient, pod)

			// Get the pod status
			ps, err := p.GetPodStatus(ctx, pod.Namespace, pod.Name)
			assert.NoError(t, err)

			// Assert the expected pod status
			vm.AssertExpectations(t)
			vzClient.AssertExpectations(t)
			golden.Assert(t, marshal(t, ps), t.Name()+".golden.yaml")
		})
	}
}

func TestGetPodStatus_MissingPod(t *testing.T) {
	ctx := context.Background()
	vg := &client.VirtualizationGroup{
		MacOSVirtualMachine: vmmocks.NewMockVirtualMachine(t),
	}

	// Mock the virtualization client
	vzClient := clientmocks.NewMockVzClientInterface(t)
	vzClient.On("GetVirtualizationGroup", mock.Anything, "test", "test").Return(vg, nil).Once()

	// Setup provider
	p := setupVZProviderWithPodInformer(t, ctx, vzClient)

	// Get the pod status
	_, err := p.GetPodStatus(ctx, "test", "test")
	assert.Error(t, err)
}

func setupVZProviderWithPodInformer(tb testing.TB, ctx context.Context, vzClient client.VzClientInterface, objects ...runtime.Object) *provider.MacOSVZProvider {
	tb.Helper()

	// Set up Kubernetes client and informers
	fakeClient := fake.NewSimpleClientset(objects...)
	podInformerFactory := informers.NewSharedInformerFactoryWithOptions(fakeClient, 1)
	podInformer := podInformerFactory.Core().V1().Pods().Informer()
	podInformerFactory.Start(ctx.Done())
	require.True(tb, cache.WaitForCacheSync(ctx.Done(), podInformer.HasSynced))

	// Set up provider
	providerConfig := provider.MacOSVZProviderConfig{
		Platform:   defaultPlatform,
		InternalIP: "10.0.0.1",
		K8sClient:  fakeClient,
		PodsLister: podInformerFactory.Core().V1().Pods().Lister(),
	}
	p, err := provider.NewMacOSVZProvider(ctx, vzClient, providerConfig)
	require.NoError(tb, err)

	return p
}

func marshal(tb testing.TB, v interface{}) string {
	tb.Helper()

	data, err := yaml.Marshal(v)
	assert.NoError(tb, err)

	return string(data)
}
