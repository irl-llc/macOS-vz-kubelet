package volumes

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// PVCResolution holds the resolved PVC, its bound PV, and the host path.
type PVCResolution struct {
	PVC      *corev1.PersistentVolumeClaim
	PV       *corev1.PersistentVolume
	HostPath string
}

// ResolvePVCs looks up all PVC volume references in a pod and resolves each to
// a host path via the bound PersistentVolume.
func ResolvePVCs(ctx context.Context, client kubernetes.Interface, pod *corev1.Pod) (map[string]*PVCResolution, error) {
	result := make(map[string]*PVCResolution)

	for _, vol := range pod.Spec.Volumes {
		if vol.PersistentVolumeClaim == nil {
			continue
		}
		resolution, err := resolveSinglePVC(ctx, client, pod.Namespace, vol.PersistentVolumeClaim)
		if err != nil {
			return nil, fmt.Errorf("resolve PVC %q: %w", vol.PersistentVolumeClaim.ClaimName, err)
		}
		result[vol.Name] = resolution
	}

	return result, nil
}

func resolveSinglePVC(
	ctx context.Context,
	client kubernetes.Interface,
	namespace string,
	claimRef *corev1.PersistentVolumeClaimVolumeSource,
) (*PVCResolution, error) {
	pvc, err := client.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, claimRef.ClaimName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get PVC: %w", err)
	}
	if pvc.Status.Phase != corev1.ClaimBound {
		return nil, fmt.Errorf("PVC %q is not bound (phase: %s)", claimRef.ClaimName, pvc.Status.Phase)
	}

	pvName := pvc.Spec.VolumeName
	if pvName == "" {
		return nil, fmt.Errorf("PVC %q has no bound volume name", claimRef.ClaimName)
	}

	pv, err := client.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get PV %q: %w", pvName, err)
	}

	hostPath, err := extractHostPath(pv)
	if err != nil {
		return nil, err
	}

	return &PVCResolution{PVC: pvc, PV: pv, HostPath: hostPath}, nil
}

// extractHostPath determines the host-side directory for the PV.
func extractHostPath(pv *corev1.PersistentVolume) (string, error) {
	if pv.Spec.HostPath != nil {
		return pv.Spec.HostPath.Path, nil
	}
	if pv.Spec.Local != nil {
		return pv.Spec.Local.Path, nil
	}
	if pv.Spec.CSI != nil {
		return extractCSIHostPath(pv)
	}
	return "", fmt.Errorf(
		"PV %q uses an unsupported volume source (only hostPath, local, and CSI are supported)",
		pv.Name,
	)
}

// extractCSIHostPath extracts the host path from a CSI PV's volume attributes.
// Many CSI hostpath-style drivers store the path under a known attribute key.
func extractCSIHostPath(pv *corev1.PersistentVolume) (string, error) {
	attrs := pv.Spec.CSI.VolumeAttributes
	for _, key := range csiPathAttributeKeys {
		if p, ok := attrs[key]; ok && p != "" {
			return p, nil
		}
	}
	return "", fmt.Errorf(
		"CSI PV %q has no recognized host path attribute; "+
			"set one of %v in volumeAttributes or use hostPath/local PVs",
		pv.Name, csiPathAttributeKeys,
	)
}

// csiPathAttributeKeys are volume-attribute keys that hostpath-style CSI drivers
// commonly use to expose the node-local directory.
var csiPathAttributeKeys = []string{
	"hostpath",
	"path",
}
