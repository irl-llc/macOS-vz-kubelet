package volumes_test

import (
	"testing"

	"github.com/agoda-com/macOS-vz-kubelet/internal/volumes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestResolveFieldRef(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-pod",
			Namespace: "my-ns",
			UID:       types.UID("abc-123"),
			Labels:    map[string]string{"app": "web", "env": "prod"},
			Annotations: map[string]string{
				"note": "hello",
			},
		},
		Spec: corev1.PodSpec{
			NodeName:           "node-1",
			ServiceAccountName: "my-sa",
		},
		Status: corev1.PodStatus{
			PodIP:  "10.0.0.5",
			HostIP: "192.168.1.1",
		},
	}

	tests := []struct {
		fieldPath string
		expected  string
	}{
		{"metadata.name", "my-pod"},
		{"metadata.namespace", "my-ns"},
		{"metadata.uid", "abc-123"},
		{"spec.nodeName", "node-1"},
		{"spec.serviceAccountName", "my-sa"},
		{"status.podIP", "10.0.0.5"},
		{"status.hostIP", "192.168.1.1"},
		{"metadata.labels['app']", "web"},
		{"metadata.labels['env']", "prod"},
		{"metadata.labels['missing']", ""},
		{"metadata.annotations['note']", "hello"},
		{"metadata.annotations['absent']", ""},
	}

	for _, tt := range tests {
		t.Run(tt.fieldPath, func(t *testing.T) {
			val, err := volumes.ResolveFieldRef(pod, tt.fieldPath)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, val)
		})
	}
}

func TestResolveFieldRef_Labels(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{"a": "1", "b": "2"},
		},
	}

	val, err := volumes.ResolveFieldRef(pod, "metadata.labels")
	require.NoError(t, err)
	assert.Contains(t, val, "a=")
	assert.Contains(t, val, "b=")
}

func TestResolveFieldRef_Unsupported(t *testing.T) {
	pod := &corev1.Pod{}
	_, err := volumes.ResolveFieldRef(pod, "spec.containers")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported fieldPath")
}

func TestResolveResourceFieldRef(t *testing.T) {
	container := corev1.Container{
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		},
	}

	tests := []struct {
		name     string
		resource string
		divisor  string
		expected string
	}{
		{"limits cpu", "limits.cpu", "1", "2"},
		{"limits memory bytes", "limits.memory", "1", "1073741824"},
		{"requests cpu millis", "requests.cpu", "1m", "500"},
		{"requests memory Mi", "requests.memory", "1Mi", "256"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := resource.MustParse(tt.divisor)
			ref := &corev1.ResourceFieldSelector{
				Resource: tt.resource,
				Divisor:  d,
			}
			val, err := volumes.ResolveResourceFieldRef(container, ref)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, val)
		})
	}
}

func TestResolveResourceFieldRef_Missing(t *testing.T) {
	container := corev1.Container{}
	ref := &corev1.ResourceFieldSelector{Resource: "limits.cpu"}
	val, err := volumes.ResolveResourceFieldRef(container, ref)
	require.NoError(t, err)
	assert.Equal(t, "0", val)
}

func TestResolveResourceFieldRef_Nil(t *testing.T) {
	container := corev1.Container{}
	_, err := volumes.ResolveResourceFieldRef(container, nil)
	assert.Error(t, err)
}

func TestResolveResourceFieldRef_Unsupported(t *testing.T) {
	container := corev1.Container{}
	ref := &corev1.ResourceFieldSelector{Resource: "limits.gpu"}
	_, err := volumes.ResolveResourceFieldRef(container, ref)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported resource")
}
