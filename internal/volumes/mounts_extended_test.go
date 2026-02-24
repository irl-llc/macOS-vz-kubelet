package volumes_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/agoda-com/macOS-vz-kubelet/internal/volumes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// --- DownwardAPI label/annotation/UID round-trip ---

func TestDownwardAPI_LabelsAnnotationsUID(t *testing.T) {
	root := t.TempDir()

	vc := volumes.VolumeContext{
		PodVolRoot: root,
		Pod: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-pod",
				Namespace: "my-ns",
				UID:       types.UID("abc-123-def"),
				Labels: map[string]string{
					"app":     "web",
					"version": "v2",
				},
				Annotations: map[string]string{
					"note": "important",
				},
			},
			Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{{
					Name: "podinfo",
					VolumeSource: corev1.VolumeSource{DownwardAPI: &corev1.DownwardAPIVolumeSource{
						Items: []corev1.DownwardAPIVolumeFile{
							{Path: "name", FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}},
							{Path: "uid", FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.uid"}},
							{Path: "labels", FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.labels"}},
							{Path: "annotations", FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.annotations"}},
						},
					}},
				}},
			},
		},
		Container: corev1.Container{VolumeMounts: []corev1.VolumeMount{
			{Name: "podinfo", MountPath: "/etc/podinfo"},
		}},
	}

	_, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.NoError(t, err)

	dir := filepath.Join(root, "podinfo")
	requireFileEquals(t, filepath.Join(dir, "name"), "my-pod")
	requireFileEquals(t, filepath.Join(dir, "uid"), "abc-123-def")

	labelsContent := readFileString(t, filepath.Join(dir, "labels"))
	assert.Contains(t, labelsContent, `app="web"`)
	assert.Contains(t, labelsContent, `version="v2"`)

	annotContent := readFileString(t, filepath.Join(dir, "annotations"))
	assert.Contains(t, annotContent, `note="important"`)
}

// --- DownwardAPI resource limits with fractional divisors ---

func TestDownwardAPI_ResourceLimitsWithDivisors(t *testing.T) {
	root := t.TempDir()

	vc := volumes.VolumeContext{
		PodVolRoot: root,
		Pod: &corev1.Pod{Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{
				Name: "resources",
				VolumeSource: corev1.VolumeSource{DownwardAPI: &corev1.DownwardAPIVolumeSource{
					Items: []corev1.DownwardAPIVolumeFile{
						{
							Path: "cpu-limit",
							ResourceFieldRef: &corev1.ResourceFieldSelector{
								ContainerName: "app",
								Resource:      "limits.cpu",
								Divisor:       resource.MustParse("1m"),
							},
						},
						{
							Path: "mem-request",
							ResourceFieldRef: &corev1.ResourceFieldSelector{
								ContainerName: "app",
								Resource:      "requests.memory",
								Divisor:       resource.MustParse("1Mi"),
							},
						},
					},
				}},
			}},
		}},
		Container: corev1.Container{
			Name: "app",
			Resources: corev1.ResourceRequirements{
				Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
			},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "resources", MountPath: "/etc/resources"},
			},
		},
	}

	_, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.NoError(t, err)

	dir := filepath.Join(root, "resources")
	requireFileEquals(t, filepath.Join(dir, "cpu-limit"), "2000")
	requireFileEquals(t, filepath.Join(dir, "mem-request"), "512")
}

// --- Optional missing Secret is skipped ---

func TestOptionalSecretMissing_Skipped(t *testing.T) {
	root := t.TempDir()

	vc := volumes.VolumeContext{
		PodVolRoot: root,
		Pod: &corev1.Pod{Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{
				Name: "opt-sec",
				VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
					SecretName: "missing-secret",
					Optional:   boolPtr(true),
				}},
			}},
		}},
		Container: corev1.Container{VolumeMounts: []corev1.VolumeMount{
			{Name: "opt-sec", MountPath: "/mnt/secret"},
		}},
	}

	mounts, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.NoError(t, err)
	assert.Empty(t, mounts, "optional missing secret should produce no mounts")
}

// --- Projected volume merging all four source types ---

func TestProjectedMerge_AllSourceTypes(t *testing.T) {
	root := t.TempDir()

	vc := volumes.VolumeContext{
		PodVolRoot:          root,
		ServiceAccountToken: "sa-token-value",
		Pod: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "test-ns",
				Labels:    map[string]string{"env": "prod"},
			},
			Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{{
					Name: "all-sources",
					VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
						Sources: []corev1.VolumeProjection{
							{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token"}},
							{ConfigMap: &corev1.ConfigMapProjection{
								LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"},
								Items:                []corev1.KeyToPath{{Key: "app.conf", Path: "config/app.conf"}},
							}},
							{Secret: &corev1.SecretProjection{
								LocalObjectReference: corev1.LocalObjectReference{Name: "tls"},
								Items:                []corev1.KeyToPath{{Key: "cert", Path: "tls/cert.pem"}},
							}},
							{DownwardAPI: &corev1.DownwardAPIProjection{
								Items: []corev1.DownwardAPIVolumeFile{{
									Path:     "namespace",
									FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
								}},
							}},
						},
					}},
				}},
			},
		},
		Container: corev1.Container{VolumeMounts: []corev1.VolumeMount{
			{Name: "all-sources", MountPath: "/mnt/proj"},
		}},
		ConfigMaps: map[string]*corev1.ConfigMap{
			"cfg": {Data: map[string]string{"app.conf": "key=value"}},
		},
		Secrets: map[string]*corev1.Secret{
			"tls": {Data: map[string][]byte{"cert": []byte("-----BEGIN CERT-----")}},
		},
	}

	mounts, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.NoError(t, err)
	require.Len(t, mounts, 1)

	dir := filepath.Join(root, "all-sources")
	requireFileEquals(t, filepath.Join(dir, "token"), "sa-token-value")
	requireFileEquals(t, filepath.Join(dir, "config", "app.conf"), "key=value")
	requireFileEquals(t, filepath.Join(dir, "tls", "cert.pem"), "-----BEGIN CERT-----")
	requireFileEquals(t, filepath.Join(dir, "namespace"), "test-ns")
}

// --- DownwardAPI specific label selector ---

func TestDownwardAPI_SpecificLabelSelector(t *testing.T) {
	root := t.TempDir()

	vc := volumes.VolumeContext{
		PodVolRoot: root,
		Pod: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					"app":     "web",
					"version": "v3",
				},
			},
			Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{{
					Name: "dapi",
					VolumeSource: corev1.VolumeSource{DownwardAPI: &corev1.DownwardAPIVolumeSource{
						Items: []corev1.DownwardAPIVolumeFile{{
							Path:     "app-label",
							FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.labels['app']"},
						}},
					}},
				}},
			},
		},
		Container: corev1.Container{VolumeMounts: []corev1.VolumeMount{
			{Name: "dapi", MountPath: "/etc/dapi"},
		}},
	}

	_, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.NoError(t, err)

	requireFileEquals(t, filepath.Join(root, "dapi", "app-label"), "web")
}

// --- helpers ---

func requireFileEquals(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err, "reading %s", path)
	assert.Equal(t, expected, string(data), "content of %s", path)
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err, "reading %s", path)
	return string(data)
}
