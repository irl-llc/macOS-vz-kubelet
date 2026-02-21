package volumes

import (
	"fmt"
	"math"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// ResolveFieldRef resolves a downwardAPI fieldRef to its string value.
func ResolveFieldRef(pod *corev1.Pod, fieldPath string) (string, error) {
	switch fieldPath {
	case "metadata.name":
		return pod.Name, nil
	case "metadata.namespace":
		return pod.Namespace, nil
	case "metadata.uid":
		return string(pod.UID), nil
	case "metadata.labels":
		return formatMap(pod.Labels), nil
	case "metadata.annotations":
		return formatMap(pod.Annotations), nil
	case "spec.nodeName":
		return pod.Spec.NodeName, nil
	case "spec.serviceAccountName":
		return pod.Spec.ServiceAccountName, nil
	case "status.podIP":
		return pod.Status.PodIP, nil
	case "status.hostIP":
		return pod.Status.HostIP, nil
	}

	if strings.HasPrefix(fieldPath, "metadata.labels['") {
		return extractMapValue(pod.Labels, fieldPath, "metadata.labels")
	}
	if strings.HasPrefix(fieldPath, "metadata.annotations['") {
		return extractMapValue(pod.Annotations, fieldPath, "metadata.annotations")
	}

	return "", fmt.Errorf("unsupported fieldPath: %s", fieldPath)
}

// ResolveResourceFieldRef resolves a downwardAPI resourceFieldRef to its string value.
func ResolveResourceFieldRef(container corev1.Container, ref *corev1.ResourceFieldSelector) (string, error) {
	if ref == nil {
		return "", fmt.Errorf("resourceFieldRef is nil")
	}

	divisor := ref.Divisor
	if divisor.IsZero() {
		divisor = resource.MustParse("1")
	}

	return resolveContainerResource(container, ref.Resource, divisor)
}

func resolveContainerResource(container corev1.Container, resourceName string, divisor resource.Quantity) (string, error) {
	var quantity resource.Quantity
	var found bool

	switch resourceName {
	case "limits.cpu":
		quantity, found = container.Resources.Limits[corev1.ResourceCPU]
	case "limits.memory":
		quantity, found = container.Resources.Limits[corev1.ResourceMemory]
	case "limits.ephemeral-storage":
		quantity, found = container.Resources.Limits[corev1.ResourceEphemeralStorage]
	case "requests.cpu":
		quantity, found = container.Resources.Requests[corev1.ResourceCPU]
	case "requests.memory":
		quantity, found = container.Resources.Requests[corev1.ResourceMemory]
	case "requests.ephemeral-storage":
		quantity, found = container.Resources.Requests[corev1.ResourceEphemeralStorage]
	default:
		return "", fmt.Errorf("unsupported resource: %s", resourceName)
	}

	if !found {
		return "0", nil
	}

	result := math.Ceil(quantity.AsApproximateFloat64() / divisor.AsApproximateFloat64())
	return fmt.Sprintf("%d", int64(result)), nil
}

func formatMap(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = fmt.Sprintf("%s=%q", k, m[k])
	}
	return strings.Join(pairs, "\n")
}

func extractMapValue(m map[string]string, fieldPath, prefix string) (string, error) {
	trimmed := strings.TrimPrefix(fieldPath, prefix+"['")
	trimmed = strings.TrimSuffix(trimmed, "']")
	if val, ok := m[trimmed]; ok {
		return val, nil
	}
	return "", nil
}
