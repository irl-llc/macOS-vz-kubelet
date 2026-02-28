package client

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
)

const (
	// VMContainersAnnotation lists container names that should run as macOS VMs.
	// Value is a comma-separated list of container names.
	// When absent, the first container is treated as a VM (backwards compatible).
	VMContainersAnnotation = "vz.kubelet.io/vm-containers"
)

// VMContainerNames returns the set of container names designated as VMs.
// When the annotation is absent, the first container is treated as a VM.
func VMContainerNames(pod *corev1.Pod) map[string]bool {
	annotation, ok := pod.Annotations[VMContainersAnnotation]
	if !ok {
		return defaultVMContainers(pod)
	}
	return parseVMAnnotation(annotation)
}

func defaultVMContainers(pod *corev1.Pod) map[string]bool {
	if len(pod.Spec.Containers) == 0 {
		return make(map[string]bool)
	}
	return map[string]bool{pod.Spec.Containers[0].Name: true}
}

func parseVMAnnotation(annotation string) map[string]bool {
	annotation = strings.TrimSpace(annotation)
	if annotation == "" {
		return make(map[string]bool)
	}
	names := map[string]bool{}
	for _, name := range strings.Split(annotation, ",") {
		trimmed := strings.TrimSpace(name)
		if trimmed != "" {
			names[trimmed] = true
		}
	}
	return names
}

// isVMContainer checks if a container is designated as a VM.
func isVMContainer(name string, vmNames map[string]bool) bool {
	return vmNames[name]
}

// hasNativeContainers checks whether any containers are not VMs.
func hasNativeContainers(pod *corev1.Pod, vmNames map[string]bool) bool {
	for _, c := range pod.Spec.Containers {
		if !vmNames[c.Name] {
			return true
		}
	}
	return false
}
