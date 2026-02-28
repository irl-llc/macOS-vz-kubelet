package client

import (
	"context"
	"testing"

	"github.com/agoda-com/macOS-vz-kubelet/pkg/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestCreateVirtualizationGroup_RejectsMultipleVMContainers verifies that
// pods annotated with more than one VM container are rejected.
func TestCreateVirtualizationGroup_RejectsMultipleVMContainers(t *testing.T) {
	c := &VzClientAPIs{cachePath: t.TempDir()}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns", Name: "pod",
			Annotations: map[string]string{
				VMContainersAnnotation: "vm-a,vm-b",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "vm-a"},
				{Name: "vm-b"},
			},
		},
	}

	err := c.CreateVirtualizationGroup(context.Background(), pod, nil, resource.RegistryCredentialStore{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at most one VM container")
}

// TestCreateVirtualizationGroup_RejectsNativeWithoutClient verifies that
// pods with native (non-VM) containers are rejected when no ContainerClient
// is configured.
func TestCreateVirtualizationGroup_RejectsNativeWithoutClient(t *testing.T) {
	c := &VzClientAPIs{cachePath: t.TempDir()}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns", Name: "pod",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "vm-main"},
				{Name: "sidecar"},
			},
		},
	}

	err := c.CreateVirtualizationGroup(context.Background(), pod, nil, resource.RegistryCredentialStore{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "native containers require a container client")
}
