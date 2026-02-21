package volumes

import corev1 "k8s.io/api/core/v1"

// PodVolumeData bundles all K8s API data needed to resolve pod volumes.
type PodVolumeData struct {
	ServiceAccountToken string
	ConfigMaps          map[string]*corev1.ConfigMap
	Secrets             map[string]*corev1.Secret
	PVCs                map[string]*PVCResolution
}
