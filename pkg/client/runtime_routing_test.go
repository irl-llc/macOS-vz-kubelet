package client

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestVMContainerNames_DefaultFirstContainer(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "vm-ctr"},
				{Name: "sidecar"},
			},
		},
	}
	names := VMContainerNames(pod)
	assert.True(t, names["vm-ctr"])
	assert.False(t, names["sidecar"])
}

func TestVMContainerNames_EmptyPod(t *testing.T) {
	pod := &corev1.Pod{}
	names := VMContainerNames(pod)
	assert.Equal(t, map[string]bool{}, names)
}

func TestVMContainerNames_Annotation(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				VMContainersAnnotation: "vm-a, vm-b",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "vm-a"},
				{Name: "vm-b"},
				{Name: "sidecar"},
			},
		},
	}
	names := VMContainerNames(pod)
	assert.True(t, names["vm-a"])
	assert.True(t, names["vm-b"])
	assert.False(t, names["sidecar"])
}

func TestVMContainerNames_EmptyAnnotation(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				VMContainersAnnotation: "",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "ctr"}},
		},
	}
	names := VMContainerNames(pod)
	assert.Equal(t, map[string]bool{}, names)
}

func TestVMContainerNames_NoneAnnotation(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				VMContainersAnnotation: "  ",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "ctr"}},
		},
	}
	names := VMContainerNames(pod)
	assert.Equal(t, map[string]bool{}, names)
}

func TestHasNativeContainers(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "vm"},
				{Name: "sidecar"},
			},
		},
	}
	vmNames := map[string]bool{"vm": true}
	assert.True(t, hasNativeContainers(pod, vmNames))
}

func TestHasNativeContainers_AllVMs(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "vm"}},
		},
	}
	vmNames := map[string]bool{"vm": true}
	assert.False(t, hasNativeContainers(pod, vmNames))
}
