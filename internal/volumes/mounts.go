package volumes

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/virtual-kubelet/virtual-kubelet/log"
	corev1 "k8s.io/api/core/v1"
)

const (
	// DefaultDirMode is the permission mode for directories created by volume resolution.
	DefaultDirMode os.FileMode = 0755
	// DefaultFileMode is the permission mode for files created by volume resolution.
	DefaultFileMode os.FileMode = 0644
)

// Mount represents a universal mount point in a container.
// Note: This is a simplified version of the actual implementation
// and can be replaced by containerd's Mount type whenever (if) containerd
// is integrated into the project.
type Mount struct {
	Name          string
	HostPath      string
	ContainerPath string
	ReadOnly      bool
}

// VolumeContext bundles all data needed for volume resolution.
type VolumeContext struct {
	PodVolRoot          string
	Pod                 *corev1.Pod
	Container           corev1.Container
	ServiceAccountToken string
	ConfigMaps          map[string]*corev1.ConfigMap
	Secrets             map[string]*corev1.Secret
	PVCs                map[string]*PVCResolution
}

// CreateContainerMounts creates the mounts for a container based on the pod spec.
func CreateContainerMounts(ctx context.Context, vc VolumeContext) ([]Mount, error) {
	var mounts []Mount
	for _, mountSpec := range vc.Container.VolumeMounts {
		podVolSpec := findPodVolumeSpec(vc.Pod, mountSpec.Name)
		if podVolSpec == nil {
			log.G(ctx).Debugf("Container volume mount %s not found in Pod spec", mountSpec.Name)
			continue
		}

		hostPath, err := resolveVolumeSource(ctx, vc, mountSpec.Name, podVolSpec)
		if err != nil {
			return nil, err
		}
		if hostPath == "" {
			continue
		}

		mounts = append(mounts, Mount{
			Name:          mountSpec.Name,
			ContainerPath: filepath.Join(mountSpec.MountPath, mountSpec.SubPath),
			ReadOnly:      mountSpec.ReadOnly,
			HostPath:      hostPath,
		})
	}
	return mounts, nil
}

func resolveVolumeSource(
	ctx context.Context,
	vc VolumeContext,
	volumeName string,
	src *corev1.VolumeSource,
) (string, error) {
	switch {
	case src.HostPath != nil:
		return resolveHostPath(src.HostPath)
	case src.EmptyDir != nil:
		return resolveEmptyDir(vc.PodVolRoot, volumeName)
	case src.Projected != nil:
		return resolveProjected(ctx, vc, volumeName, src.Projected)
	case src.ConfigMap != nil:
		return resolveConfigMap(vc, volumeName, src.ConfigMap)
	case src.Secret != nil:
		return resolveSecret(vc, volumeName, src.Secret)
	case src.DownwardAPI != nil:
		return resolveDownwardAPIVolume(vc, volumeName, src.DownwardAPI)
	case src.PersistentVolumeClaim != nil:
		return resolvePVCVolume(vc, volumeName, src.PersistentVolumeClaim)
	default:
		return "", nil
	}
}

func resolveHostPath(hp *corev1.HostPathVolumeSource) (string, error) {
	hostPathType := corev1.HostPathUnset
	if hp.Type != nil {
		hostPathType = *hp.Type
	}
	return resolveHostPathByType(hp.Path, hostPathType)
}

func resolveHostPathByType(path string, hpType corev1.HostPathType) (string, error) {
	switch hpType {
	case corev1.HostPathUnset, corev1.HostPathDirectoryOrCreate:
		return path, os.MkdirAll(path, DefaultDirMode)
	case corev1.HostPathDirectory:
		return path, verifyDirectory(path)
	case corev1.HostPathFileOrCreate:
		return path, ensureFileExists(path)
	case corev1.HostPathFile:
		return path, verifyFileExists(path)
	default:
		return "", fmt.Errorf("unsupported hostPath type: %q", hpType)
	}
}

func verifyDirectory(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("hostPath %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("hostPath %s is not a directory", path)
	}
	return nil
}

func ensureFileExists(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), DefaultDirMode); err != nil {
		return fmt.Errorf("hostPath parent %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, DefaultFileMode)
	if err != nil {
		return fmt.Errorf("hostPath %s: %w", path, err)
	}
	return f.Close()
}

func verifyFileExists(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("hostPath %s: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("hostPath %s is a directory, not a file", path)
	}
	return nil
}

func resolveEmptyDir(podVolRoot, volumeName string) (string, error) {
	hostPath := filepath.Join(podVolRoot, volumeName)
	if err := os.MkdirAll(hostPath, DefaultDirMode); err != nil {
		return "", fmt.Errorf("make emptyDir %s: %w", hostPath, err)
	}
	return hostPath, nil
}

func resolveProjected(
	ctx context.Context,
	vc VolumeContext,
	volumeName string,
	projected *corev1.ProjectedVolumeSource,
) (string, error) {
	hostPath := filepath.Join(vc.PodVolRoot, volumeName)
	if err := os.MkdirAll(hostPath, DefaultDirMode); err != nil {
		return "", fmt.Errorf("make projected %s: %w", hostPath, err)
	}
	for _, source := range projected.Sources {
		if err := writeProjectionSource(ctx, vc, hostPath, source); err != nil {
			return "", err
		}
	}
	return hostPath, nil
}

func writeProjectionSource(
	_ context.Context,
	vc VolumeContext,
	hostPath string,
	source corev1.VolumeProjection,
) error {
	if source.ServiceAccountToken != nil {
		return writeServiceAccountToken(hostPath, source.ServiceAccountToken, vc.ServiceAccountToken)
	}
	if source.ConfigMap != nil {
		return writeConfigMapProjection(hostPath, source.ConfigMap, vc.ConfigMaps)
	}
	if source.Secret != nil {
		return writeSecretProjection(hostPath, source.Secret, vc.Secrets)
	}
	if source.DownwardAPI != nil {
		return writeDownwardAPIItems(hostPath, source.DownwardAPI.Items, DefaultFileMode, vc)
	}
	return nil
}

func writeServiceAccountToken(hostPath string, proj *corev1.ServiceAccountTokenProjection, token string) error {
	dest := filepath.Join(hostPath, proj.Path)
	if err := os.MkdirAll(filepath.Dir(dest), DefaultDirMode); err != nil {
		return fmt.Errorf("write service account token dir: %w", err)
	}
	if err := os.WriteFile(dest, []byte(token), DefaultFileMode); err != nil {
		return fmt.Errorf("write service account token: %w", err)
	}
	return nil
}

func writeConfigMapProjection(
	hostPath string,
	proj *corev1.ConfigMapProjection,
	configMaps map[string]*corev1.ConfigMap,
) error {
	cm := configMaps[proj.Name]
	isOptional := proj.Optional != nil && *proj.Optional
	if cm == nil {
		if isOptional {
			return nil
		}
		return fmt.Errorf("config map %q not found", proj.Name)
	}
	return writeConfigMapData(hostPath, cm, proj.Items)
}

func writeSecretProjection(
	hostPath string,
	proj *corev1.SecretProjection,
	secrets map[string]*corev1.Secret,
) error {
	secret := secrets[proj.Name]
	isOptional := proj.Optional != nil && *proj.Optional
	if secret == nil {
		if isOptional {
			return nil
		}
		return fmt.Errorf("secret %q not found", proj.Name)
	}
	return writeSecretData(hostPath, secret, proj.Items)
}

func resolveDownwardAPIItem(vc VolumeContext, item corev1.DownwardAPIVolumeFile) (string, error) {
	if item.FieldRef != nil {
		return ResolveFieldRef(vc.Pod, item.FieldRef.FieldPath)
	}
	if item.ResourceFieldRef != nil {
		return ResolveResourceFieldRef(vc.Container, item.ResourceFieldRef)
	}
	return "", fmt.Errorf("downward API item has neither fieldRef nor resourceFieldRef")
}

func resolveConfigMap(vc VolumeContext, volumeName string, src *corev1.ConfigMapVolumeSource) (string, error) {
	cm := vc.ConfigMaps[src.Name]
	isOptional := src.Optional != nil && *src.Optional
	if cm == nil {
		if isOptional {
			return "", nil
		}
		return "", fmt.Errorf("config map %q not found", src.Name)
	}

	hostPath := filepath.Join(vc.PodVolRoot, volumeName)
	if err := os.MkdirAll(hostPath, DefaultDirMode); err != nil {
		return "", fmt.Errorf("make configMap dir %s: %w", hostPath, err)
	}
	if err := writeConfigMapData(hostPath, cm, src.Items); err != nil {
		return "", err
	}
	return hostPath, nil
}

func resolveSecret(vc VolumeContext, volumeName string, src *corev1.SecretVolumeSource) (string, error) {
	secret := vc.Secrets[src.SecretName]
	isOptional := src.Optional != nil && *src.Optional
	if secret == nil {
		if isOptional {
			return "", nil
		}
		return "", fmt.Errorf("secret %q not found", src.SecretName)
	}

	hostPath := filepath.Join(vc.PodVolRoot, volumeName)
	if err := os.MkdirAll(hostPath, DefaultDirMode); err != nil {
		return "", fmt.Errorf("make secret dir %s: %w", hostPath, err)
	}
	if err := writeSecretData(hostPath, secret, src.Items); err != nil {
		return "", err
	}
	return hostPath, nil
}

func resolveDownwardAPIVolume(vc VolumeContext, volumeName string, src *corev1.DownwardAPIVolumeSource) (string, error) {
	hostPath := filepath.Join(vc.PodVolRoot, volumeName)
	if err := os.MkdirAll(hostPath, DefaultDirMode); err != nil {
		return "", fmt.Errorf("make downwardAPI dir %s: %w", hostPath, err)
	}
	defaultMode := defaultModeOr(src.DefaultMode, DefaultFileMode)
	if err := writeDownwardAPIItems(hostPath, src.Items, defaultMode, vc); err != nil {
		return "", err
	}
	return hostPath, nil
}

// writeDownwardAPIItems writes a slice of DownwardAPIVolumeFile entries to disk.
func writeDownwardAPIItems(
	hostPath string,
	items []corev1.DownwardAPIVolumeFile,
	defaultMode os.FileMode,
	vc VolumeContext,
) error {
	for _, item := range items {
		if err := writeDownwardAPIItem(hostPath, item, defaultMode, vc); err != nil {
			return err
		}
	}
	return nil
}

func writeDownwardAPIItem(
	hostPath string,
	item corev1.DownwardAPIVolumeFile,
	defaultMode os.FileMode,
	vc VolumeContext,
) error {
	value, err := resolveDownwardAPIItem(vc, item)
	if err != nil {
		return err
	}
	mode := defaultMode
	if item.Mode != nil {
		mode = os.FileMode(*item.Mode)
	}
	dest := filepath.Join(hostPath, item.Path)
	if err := os.MkdirAll(filepath.Dir(dest), DefaultDirMode); err != nil {
		return fmt.Errorf("write downwardAPI dir: %w", err)
	}
	return os.WriteFile(dest, []byte(value), mode)
}

func resolvePVCVolume(
	vc VolumeContext,
	volumeName string,
	_ *corev1.PersistentVolumeClaimVolumeSource,
) (string, error) {
	resolution := vc.PVCs[volumeName]
	if resolution == nil {
		return "", fmt.Errorf("PVC resolution for volume %q not found", volumeName)
	}
	return resolution.HostPath, nil
}

// writeConfigMapData writes config map data to the host path. When items is non-empty,
// only the specified keys are written; otherwise all keys are written.
func writeConfigMapData(hostPath string, cm *corev1.ConfigMap, items []corev1.KeyToPath) error {
	if len(items) > 0 {
		return writeKeyedEntries(hostPath, items, func(k string) []byte { return []byte(cm.Data[k]) })
	}
	for key, value := range cm.Data {
		if err := os.WriteFile(filepath.Join(hostPath, key), []byte(value), DefaultFileMode); err != nil {
			return fmt.Errorf("write configMap key %s: %w", key, err)
		}
	}
	return nil
}

// writeSecretData writes secret data to the host path. When items is non-empty,
// only the specified keys are written; otherwise all keys are written.
func writeSecretData(hostPath string, secret *corev1.Secret, items []corev1.KeyToPath) error {
	if len(items) > 0 {
		return writeKeyedEntries(hostPath, items, func(k string) []byte { return secret.Data[k] })
	}
	for key, value := range secret.Data {
		if err := os.WriteFile(filepath.Join(hostPath, key), value, DefaultFileMode); err != nil {
			return fmt.Errorf("write secret key %s: %w", key, err)
		}
	}
	return nil
}

// writeKeyedEntries writes selected key/path entries using a lookup function.
func writeKeyedEntries(hostPath string, items []corev1.KeyToPath, lookup func(string) []byte) error {
	for _, item := range items {
		mode := DefaultFileMode
		if item.Mode != nil {
			mode = os.FileMode(*item.Mode)
		}
		dest := filepath.Join(hostPath, item.Path)
		if err := os.MkdirAll(filepath.Dir(dest), DefaultDirMode); err != nil {
			return fmt.Errorf("write dir for key %s: %w", item.Key, err)
		}
		if err := os.WriteFile(dest, lookup(item.Key), mode); err != nil {
			return fmt.Errorf("write key %s: %w", item.Key, err)
		}
	}
	return nil
}

func defaultModeOr(mode *int32, fallback os.FileMode) os.FileMode {
	if mode != nil {
		return os.FileMode(*mode)
	}
	return fallback
}

// findPodVolumeSpec searches for a particular volume spec by name in the Pod spec.
func findPodVolumeSpec(pod *corev1.Pod, name string) *corev1.VolumeSource {
	for _, volume := range pod.Spec.Volumes {
		if volume.Name == name {
			return &volume.VolumeSource
		}
	}
	return nil
}
