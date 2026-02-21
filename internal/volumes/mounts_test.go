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
)

func boolPtr(b bool) *bool     { return &b }
func int32Ptr(i int32) *int32   { return &i }

func hostPathType(t corev1.HostPathType) *corev1.HostPathType { return &t }

func requireFileContent(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err, "reading %s", path)
	assert.Equal(t, expected, string(data), "content of %s", path)
}

func requireFileMode(t *testing.T, path string, expected os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err, "stat %s", path)
	assert.Equal(t, expected, info.Mode().Perm(), "mode of %s", path)
}

func requireDirExists(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err, "stat %s", path)
	assert.True(t, info.IsDir(), "%s should be a directory", path)
}

func TestCreateContainerMounts(t *testing.T) {
	tempDir := t.TempDir()

	tests := []struct {
		name                string
		container           corev1.Container
		pod                 *corev1.Pod
		serviceAccountToken string
		configMaps          map[string]*corev1.ConfigMap
		secrets             map[string]*corev1.Secret
		pvcs                map[string]*volumes.PVCResolution
		expectedMounts      []volumes.Mount
		expectError         bool
	}{
		{
			name: "HostPath volume",
			container: corev1.Container{
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "test-volume",
						MountPath: "/mnt/test",
					},
				},
			},
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "test-volume",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/tmp/hostpath",
								},
							},
						},
					},
				},
			},
			expectedMounts: []volumes.Mount{
				{
					Name:          "test-volume",
					HostPath:      "/tmp/hostpath",
					ContainerPath: "/mnt/test",
					ReadOnly:      false,
				},
			},
		},
		{
			name: "EmptyDir volume",
			container: corev1.Container{
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "emptydir-volume",
						MountPath: "/mnt/emptydir",
					},
				},
			},
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "emptydir-volume",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
			expectedMounts: []volumes.Mount{
				{
					Name:          "emptydir-volume",
					HostPath:      filepath.Join(tempDir, "emptydir-volume"),
					ContainerPath: "/mnt/emptydir",
					ReadOnly:      false,
				},
			},
		},
		{
			name: "Projected volume with ServiceAccountToken",
			container: corev1.Container{
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "projected-volume",
						MountPath: "/mnt/projected",
					},
				},
			},
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "test-namespace",
				},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "projected-volume",
							VolumeSource: corev1.VolumeSource{
								Projected: &corev1.ProjectedVolumeSource{
									Sources: []corev1.VolumeProjection{
										{
											ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
												Path: "token",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			serviceAccountToken: "test-token",
			expectedMounts: []volumes.Mount{
				{
					Name:          "projected-volume",
					HostPath:      filepath.Join(tempDir, "projected-volume"),
					ContainerPath: "/mnt/projected",
					ReadOnly:      false,
				},
			},
		},
		{
			name: "Projected volume with ConfigMap",
			container: corev1.Container{
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "configmap-volume",
						MountPath: "/mnt/configmap",
					},
				},
			},
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "configmap-volume",
							VolumeSource: corev1.VolumeSource{
								Projected: &corev1.ProjectedVolumeSource{
									Sources: []corev1.VolumeProjection{
										{
											ConfigMap: &corev1.ConfigMapProjection{
												LocalObjectReference: corev1.LocalObjectReference{
													Name: "test-configmap",
												},
												Items: []corev1.KeyToPath{
													{
														Key:  "config-key",
														Path: "config-path",
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			configMaps: map[string]*corev1.ConfigMap{
				"test-configmap": {
					Data: map[string]string{
						"config-key": "config-value",
					},
				},
			},
			expectedMounts: []volumes.Mount{
				{
					Name:          "configmap-volume",
					HostPath:      filepath.Join(tempDir, "configmap-volume"),
					ContainerPath: "/mnt/configmap",
					ReadOnly:      false,
				},
			},
		},
		{
			name: "Projected volume with DownwardAPI (namespace)",
			container: corev1.Container{
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "downwardapi-volume",
						MountPath: "/mnt/downwardapi",
					},
				},
			},
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "test-namespace",
				},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "downwardapi-volume",
							VolumeSource: corev1.VolumeSource{
								Projected: &corev1.ProjectedVolumeSource{
									Sources: []corev1.VolumeProjection{
										{
											DownwardAPI: &corev1.DownwardAPIProjection{
												Items: []corev1.DownwardAPIVolumeFile{
													{
														Path: "namespace",
														FieldRef: &corev1.ObjectFieldSelector{
															FieldPath: "metadata.namespace",
														},
														Mode: func(i int32) *int32 {
															return &i
														}(0644),
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			expectedMounts: []volumes.Mount{
				{
					Name:          "downwardapi-volume",
					HostPath:      filepath.Join(tempDir, "downwardapi-volume"),
					ContainerPath: "/mnt/downwardapi",
					ReadOnly:      false,
				},
			},
		},
		{
			name: "Standalone ConfigMap volume",
			container: corev1.Container{
				VolumeMounts: []corev1.VolumeMount{
					{Name: "cm-vol", MountPath: "/etc/config"},
				},
			},
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "cm-vol",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"},
								},
							},
						},
					},
				},
			},
			configMaps: map[string]*corev1.ConfigMap{
				"app-config": {
					Data: map[string]string{"key1": "val1", "key2": "val2"},
				},
			},
			expectedMounts: []volumes.Mount{
				{
					Name:          "cm-vol",
					HostPath:      filepath.Join(tempDir, "cm-vol"),
					ContainerPath: "/etc/config",
				},
			},
		},
		{
			name: "Standalone Secret volume",
			container: corev1.Container{
				VolumeMounts: []corev1.VolumeMount{
					{Name: "sec-vol", MountPath: "/etc/secret"},
				},
			},
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "sec-vol",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: "app-secret",
								},
							},
						},
					},
				},
			},
			secrets: map[string]*corev1.Secret{
				"app-secret": {
					Data: map[string][]byte{"password": []byte("s3cret")},
				},
			},
			expectedMounts: []volumes.Mount{
				{
					Name:          "sec-vol",
					HostPath:      filepath.Join(tempDir, "sec-vol"),
					ContainerPath: "/etc/secret",
				},
			},
		},
		{
			name: "DownwardAPI volume",
			container: corev1.Container{
				VolumeMounts: []corev1.VolumeMount{
					{Name: "dapi-vol", MountPath: "/etc/podinfo"},
				},
			},
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-pod",
					Namespace: "my-ns",
				},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "dapi-vol",
							VolumeSource: corev1.VolumeSource{
								DownwardAPI: &corev1.DownwardAPIVolumeSource{
									Items: []corev1.DownwardAPIVolumeFile{
										{
											Path:     "name",
											FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
										},
									},
								},
							},
						},
					},
				},
			},
			expectedMounts: []volumes.Mount{
				{
					Name:          "dapi-vol",
					HostPath:      filepath.Join(tempDir, "dapi-vol"),
					ContainerPath: "/etc/podinfo",
				},
			},
		},
		{
			name: "PVC volume",
			container: corev1.Container{
				VolumeMounts: []corev1.VolumeMount{
					{Name: "pvc-vol", MountPath: "/data"},
				},
			},
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "pvc-vol",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: "my-pvc",
								},
							},
						},
					},
				},
			},
			pvcs: map[string]*volumes.PVCResolution{
				"pvc-vol": {HostPath: "/host/data"},
			},
			expectedMounts: []volumes.Mount{
				{
					Name:          "pvc-vol",
					HostPath:      "/host/data",
					ContainerPath: "/data",
				},
			},
		},
		{
			name: "Optional missing ConfigMap skipped",
			container: corev1.Container{
				VolumeMounts: []corev1.VolumeMount{
					{Name: "opt-cm", MountPath: "/opt"},
				},
			},
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "opt-cm",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: "missing"},
									Optional:             boolPtr(true),
								},
							},
						},
					},
				},
			},
			expectedMounts: nil,
		},
		{
			name: "Required missing Secret errors",
			container: corev1.Container{
				VolumeMounts: []corev1.VolumeMount{
					{Name: "req-sec", MountPath: "/secret"},
				},
			},
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "req-sec",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: "missing-secret",
								},
							},
						},
					},
				},
			},
			expectError: true,
		},
		{
			name: "Volume not found in Pod spec",
			container: corev1.Container{
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "non-existent-volume",
						MountPath: "/mnt/non-existent",
					},
				},
			},
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{},
				},
			},
			expectedMounts: nil,
		},
		{
			name: "Unsupported volume type",
			container: corev1.Container{
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "unsupported-volume",
						MountPath: "/mnt/unsupported",
					},
				},
			},
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "unsupported-volume",
							VolumeSource: corev1.VolumeSource{
								// NFS is an unsupported volume type
								NFS: &corev1.NFSVolumeSource{
									Server: "nfs.example.com",
									Path:   "/exports/data",
								},
							},
						},
					},
				},
			},
			expectedMounts: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vc := volumes.VolumeContext{
				PodVolRoot:          tempDir,
				Pod:                 tt.pod,
				Container:           tt.container,
				ServiceAccountToken: tt.serviceAccountToken,
				ConfigMaps:          tt.configMaps,
				Secrets:             tt.secrets,
				PVCs:                tt.pvcs,
			}
			mounts, err := volumes.CreateContainerMounts(context.Background(), vc)
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedMounts, mounts)
			}
		})
	}
}

// --- File content verification ---

func TestFileContentVerification_ConfigMap(t *testing.T) {
	root := t.TempDir()
	vc := configMapVolumeContext(root, "cm-vol", "app-cfg",
		map[string]string{"key1": "val1", "key2": "val2"}, nil)

	_, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.NoError(t, err)

	requireFileContent(t, filepath.Join(root, "cm-vol", "key1"), "val1")
	requireFileContent(t, filepath.Join(root, "cm-vol", "key2"), "val2")
}

func TestFileContentVerification_Secret(t *testing.T) {
	root := t.TempDir()
	vc := secretVolumeContext(root, "sec-vol", "app-sec",
		map[string][]byte{"password": []byte("s3cret")}, nil)

	_, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.NoError(t, err)

	requireFileContent(t, filepath.Join(root, "sec-vol", "password"), "s3cret")
}

func TestFileContentVerification_SAToken(t *testing.T) {
	root := t.TempDir()
	vc := saTokenVolumeContext(root, "proj-vol", "my-token")

	_, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.NoError(t, err)

	requireFileContent(t, filepath.Join(root, "proj-vol", "token"), "my-token")
}

func TestFileContentVerification_DownwardAPI(t *testing.T) {
	root := t.TempDir()
	vc := downwardAPIFieldVolumeContext(root, "dapi-vol", "name", "metadata.name", "my-pod")

	_, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.NoError(t, err)

	requireFileContent(t, filepath.Join(root, "dapi-vol", "name"), "my-pod")
}

func TestFileContentVerification_ConfigMapWithItems(t *testing.T) {
	root := t.TempDir()
	items := []corev1.KeyToPath{{Key: "k1", Path: "custom/path.txt"}}
	vc := configMapVolumeContext(root, "cm-vol", "app-cfg",
		map[string]string{"k1": "v1", "k2": "v2"}, items)

	_, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.NoError(t, err)

	requireFileContent(t, filepath.Join(root, "cm-vol", "custom", "path.txt"), "v1")
	// k2 should not be written when Items is specified
	_, err = os.Stat(filepath.Join(root, "cm-vol", "k2"))
	assert.True(t, os.IsNotExist(err), "k2 should not be written when Items is set")
}

func TestFileContentVerification_SecretWithItems(t *testing.T) {
	root := t.TempDir()
	items := []corev1.KeyToPath{{Key: "pw", Path: "creds/pass"}}
	vc := secretVolumeContext(root, "sec-vol", "app-sec",
		map[string][]byte{"pw": []byte("s3cret"), "extra": []byte("nope")}, items)

	_, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.NoError(t, err)

	requireFileContent(t, filepath.Join(root, "sec-vol", "creds", "pass"), "s3cret")
	_, err = os.Stat(filepath.Join(root, "sec-vol", "extra"))
	assert.True(t, os.IsNotExist(err), "extra should not be written when Items is set")
}

// --- HostPath type variants ---

func TestHostPathTypes_DirectoryOrCreate(t *testing.T) {
	root := t.TempDir()
	dirPath := filepath.Join(root, "newdir")
	vc := hostPathVolumeContext(dirPath, hostPathType(corev1.HostPathDirectoryOrCreate))

	mounts, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.NoError(t, err)
	require.Len(t, mounts, 1)
	requireDirExists(t, dirPath)
}

func TestHostPathTypes_Directory_Exists(t *testing.T) {
	dir := t.TempDir()
	vc := hostPathVolumeContext(dir, hostPathType(corev1.HostPathDirectory))

	mounts, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.NoError(t, err)
	require.Len(t, mounts, 1)
	assert.Equal(t, dir, mounts[0].HostPath)
}

func TestHostPathTypes_Directory_Missing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	vc := hostPathVolumeContext(missing, hostPathType(corev1.HostPathDirectory))

	_, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hostPath")
}

func TestHostPathTypes_Directory_IsFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "afile")
	require.NoError(t, os.WriteFile(f, []byte("x"), 0644))
	vc := hostPathVolumeContext(f, hostPathType(corev1.HostPathDirectory))

	_, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

func TestHostPathTypes_FileOrCreate(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "sub", "newfile")
	vc := hostPathVolumeContext(filePath, hostPathType(corev1.HostPathFileOrCreate))

	mounts, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.NoError(t, err)
	require.Len(t, mounts, 1)

	info, err := os.Stat(filePath)
	require.NoError(t, err)
	assert.False(t, info.IsDir(), "should be a file, not a directory")
}

func TestHostPathTypes_File_Exists(t *testing.T) {
	f := filepath.Join(t.TempDir(), "afile")
	require.NoError(t, os.WriteFile(f, []byte("x"), 0644))
	vc := hostPathVolumeContext(f, hostPathType(corev1.HostPathFile))

	mounts, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.NoError(t, err)
	require.Len(t, mounts, 1)
	assert.Equal(t, f, mounts[0].HostPath)
}

func TestHostPathTypes_File_Missing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	vc := hostPathVolumeContext(missing, hostPathType(corev1.HostPathFile))

	_, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hostPath")
}

func TestHostPathTypes_File_IsDirectory(t *testing.T) {
	dir := t.TempDir()
	vc := hostPathVolumeContext(dir, hostPathType(corev1.HostPathFile))

	_, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is a directory")
}

// --- File permission verification ---

func TestFilePermissions_DefaultFileMode(t *testing.T) {
	root := t.TempDir()
	vc := configMapVolumeContext(root, "cm-vol", "cfg",
		map[string]string{"key": "val"}, nil)

	_, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.NoError(t, err)
	requireFileMode(t, filepath.Join(root, "cm-vol", "key"), 0644)
}

func TestFilePermissions_DefaultDirMode(t *testing.T) {
	root := t.TempDir()
	vc := emptyDirVolumeContext(root, "ed-vol")

	_, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.NoError(t, err)
	requireFileMode(t, filepath.Join(root, "ed-vol"), 0755)
}

func TestFilePermissions_CustomKeyToPathMode(t *testing.T) {
	root := t.TempDir()
	items := []corev1.KeyToPath{{Key: "k", Path: "k", Mode: int32Ptr(0600)}}
	vc := configMapVolumeContext(root, "cm-vol", "cfg",
		map[string]string{"k": "v"}, items)

	_, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.NoError(t, err)
	requireFileMode(t, filepath.Join(root, "cm-vol", "k"), 0600)
}

func TestFilePermissions_CustomDownwardAPIFileMode(t *testing.T) {
	root := t.TempDir()
	vc := downwardAPIVolumeContextWithMode(root, "dapi-vol", "ns",
		"metadata.namespace", "my-ns", int32Ptr(0444), nil)

	_, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.NoError(t, err)
	requireFileMode(t, filepath.Join(root, "dapi-vol", "ns"), 0444)
}

func TestFilePermissions_DownwardAPIDefaultMode(t *testing.T) {
	root := t.TempDir()
	vc := downwardAPIVolumeContextWithMode(root, "dapi-vol", "ns",
		"metadata.namespace", "my-ns", nil, int32Ptr(0400))

	_, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.NoError(t, err)
	requireFileMode(t, filepath.Join(root, "dapi-vol", "ns"), 0400)
}

// --- Projected secret ---

func TestProjectedSecret(t *testing.T) {
	root := t.TempDir()
	vc := projectedSecretVolumeContext(root, "proj-vol", "my-sec",
		[]corev1.KeyToPath{{Key: "cert", Path: "tls.crt"}},
		map[string][]byte{"cert": []byte("pem-data")})

	_, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.NoError(t, err)
	requireFileContent(t, filepath.Join(root, "proj-vol", "tls.crt"), "pem-data")
}

// --- Multi-source projection ---

func TestMultiSourceProjection(t *testing.T) {
	root := t.TempDir()
	vc := multiSourceProjectionContext(root)

	_, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.NoError(t, err)

	projDir := filepath.Join(root, "proj-vol")
	requireFileContent(t, filepath.Join(projDir, "token"), "sa-tok")
	requireFileContent(t, filepath.Join(projDir, "config.yaml"), "cfg-val")
	requireFileContent(t, filepath.Join(projDir, "namespace"), "my-ns")
}

// --- SubPath and ReadOnly ---

func TestSubPathPropagation(t *testing.T) {
	root := t.TempDir()
	vc := volumes.VolumeContext{
		PodVolRoot: root,
		Pod: &corev1.Pod{Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{
				Name:         "ed",
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			}},
		}},
		Container: corev1.Container{VolumeMounts: []corev1.VolumeMount{
			{Name: "ed", MountPath: "/mnt/data", SubPath: "subdir"},
		}},
	}

	mounts, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.NoError(t, err)
	require.Len(t, mounts, 1)
	assert.Equal(t, "/mnt/data/subdir", mounts[0].ContainerPath)
}

func TestReadOnlyMount(t *testing.T) {
	root := t.TempDir()
	vc := volumes.VolumeContext{
		PodVolRoot: root,
		Pod: &corev1.Pod{Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{
				Name:         "ed",
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			}},
		}},
		Container: corev1.Container{VolumeMounts: []corev1.VolumeMount{
			{Name: "ed", MountPath: "/data", ReadOnly: true},
		}},
	}

	mounts, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.NoError(t, err)
	require.Len(t, mounts, 1)
	assert.True(t, mounts[0].ReadOnly)
}

// --- DownwardAPI resource fields through mount ---

func TestDownwardAPIResourceFields_CPULimit(t *testing.T) {
	root := t.TempDir()
	vc := downwardAPIResourceVolumeContext(root, "dapi-vol",
		"cpu-limit", "limits.cpu", "1",
		corev1.ResourceRequirements{
			Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
		})

	_, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.NoError(t, err)
	requireFileContent(t, filepath.Join(root, "dapi-vol", "cpu-limit"), "2")
}

func TestDownwardAPIResourceFields_MemoryRequest(t *testing.T) {
	root := t.TempDir()
	vc := downwardAPIResourceVolumeContext(root, "dapi-vol",
		"mem-req", "requests.memory", "1Mi",
		corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
		})

	_, err := volumes.CreateContainerMounts(context.Background(), vc)
	require.NoError(t, err)
	requireFileContent(t, filepath.Join(root, "dapi-vol", "mem-req"), "256")
}

// --- VolumeContext builders ---

func configMapVolumeContext(
	root, volName, cmName string,
	data map[string]string,
	items []corev1.KeyToPath,
) volumes.VolumeContext {
	return volumes.VolumeContext{
		PodVolRoot: root,
		Pod: &corev1.Pod{Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{
				Name: volName,
				VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
					Items:                items,
				}},
			}},
		}},
		Container: corev1.Container{VolumeMounts: []corev1.VolumeMount{
			{Name: volName, MountPath: "/mnt/" + volName},
		}},
		ConfigMaps: map[string]*corev1.ConfigMap{cmName: {Data: data}},
	}
}

func secretVolumeContext(
	root, volName, secName string,
	data map[string][]byte,
	items []corev1.KeyToPath,
) volumes.VolumeContext {
	return volumes.VolumeContext{
		PodVolRoot: root,
		Pod: &corev1.Pod{Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{
				Name: volName,
				VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
					SecretName: secName,
					Items:      items,
				}},
			}},
		}},
		Container: corev1.Container{VolumeMounts: []corev1.VolumeMount{
			{Name: volName, MountPath: "/mnt/" + volName},
		}},
		Secrets: map[string]*corev1.Secret{secName: {Data: data}},
	}
}

func saTokenVolumeContext(root, volName, token string) volumes.VolumeContext {
	return volumes.VolumeContext{
		PodVolRoot:          root,
		ServiceAccountToken: token,
		Pod: &corev1.Pod{Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{
				Name: volName,
				VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
					Sources: []corev1.VolumeProjection{
						{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token"}},
					},
				}},
			}},
		}},
		Container: corev1.Container{VolumeMounts: []corev1.VolumeMount{
			{Name: volName, MountPath: "/mnt/" + volName},
		}},
	}
}

func emptyDirVolumeContext(root, volName string) volumes.VolumeContext {
	return volumes.VolumeContext{
		PodVolRoot: root,
		Pod: &corev1.Pod{Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{
				Name:         volName,
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			}},
		}},
		Container: corev1.Container{VolumeMounts: []corev1.VolumeMount{
			{Name: volName, MountPath: "/mnt/" + volName},
		}},
	}
}

func hostPathVolumeContext(
	path string, hpType *corev1.HostPathType,
) volumes.VolumeContext {
	return volumes.VolumeContext{
		Pod: &corev1.Pod{Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{
				Name: "hp-vol",
				VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
					Path: path,
					Type: hpType,
				}},
			}},
		}},
		Container: corev1.Container{VolumeMounts: []corev1.VolumeMount{
			{Name: "hp-vol", MountPath: "/mnt/hp"},
		}},
	}
}

func downwardAPIFieldVolumeContext(
	root, volName, filePath, fieldPath, podName string,
) volumes.VolumeContext {
	return volumes.VolumeContext{
		PodVolRoot: root,
		Pod: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: podName},
			Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{{
					Name: volName,
					VolumeSource: corev1.VolumeSource{DownwardAPI: &corev1.DownwardAPIVolumeSource{
						Items: []corev1.DownwardAPIVolumeFile{{
							Path:     filePath,
							FieldRef: &corev1.ObjectFieldSelector{FieldPath: fieldPath},
						}},
					}},
				}},
			},
		},
		Container: corev1.Container{VolumeMounts: []corev1.VolumeMount{
			{Name: volName, MountPath: "/mnt/" + volName},
		}},
	}
}

func downwardAPIVolumeContextWithMode(
	root, volName, filePath, fieldPath, nsName string,
	itemMode, defaultMode *int32,
) volumes.VolumeContext {
	return volumes.VolumeContext{
		PodVolRoot: root,
		Pod: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: nsName},
			Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{{
					Name: volName,
					VolumeSource: corev1.VolumeSource{DownwardAPI: &corev1.DownwardAPIVolumeSource{
						DefaultMode: defaultMode,
						Items: []corev1.DownwardAPIVolumeFile{{
							Path:     filePath,
							FieldRef: &corev1.ObjectFieldSelector{FieldPath: fieldPath},
							Mode:     itemMode,
						}},
					}},
				}},
			},
		},
		Container: corev1.Container{VolumeMounts: []corev1.VolumeMount{
			{Name: volName, MountPath: "/mnt/" + volName},
		}},
	}
}

func downwardAPIResourceVolumeContext(
	root, volName, filePath, resourceField, divisor string,
	resources corev1.ResourceRequirements,
) volumes.VolumeContext {
	d := resource.MustParse(divisor)
	return volumes.VolumeContext{
		PodVolRoot: root,
		Pod: &corev1.Pod{Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{
				Name: volName,
				VolumeSource: corev1.VolumeSource{DownwardAPI: &corev1.DownwardAPIVolumeSource{
					Items: []corev1.DownwardAPIVolumeFile{{
						Path: filePath,
						ResourceFieldRef: &corev1.ResourceFieldSelector{
							Resource:      resourceField,
							Divisor:       d,
							ContainerName: "app",
						},
					}},
				}},
			}},
		}},
		Container: corev1.Container{
			Name:      "app",
			Resources: resources,
			VolumeMounts: []corev1.VolumeMount{
				{Name: volName, MountPath: "/mnt/" + volName},
			},
		},
	}
}

func projectedSecretVolumeContext(
	root, volName, secName string,
	items []corev1.KeyToPath,
	data map[string][]byte,
) volumes.VolumeContext {
	return volumes.VolumeContext{
		PodVolRoot: root,
		Pod: &corev1.Pod{Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{
				Name: volName,
				VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
					Sources: []corev1.VolumeProjection{{
						Secret: &corev1.SecretProjection{
							LocalObjectReference: corev1.LocalObjectReference{Name: secName},
							Items:                items,
						},
					}},
				}},
			}},
		}},
		Container: corev1.Container{VolumeMounts: []corev1.VolumeMount{
			{Name: volName, MountPath: "/mnt/" + volName},
		}},
		Secrets: map[string]*corev1.Secret{secName: {Data: data}},
	}
}

func multiSourceProjectionContext(root string) volumes.VolumeContext {
	return volumes.VolumeContext{
		PodVolRoot:          root,
		ServiceAccountToken: "sa-tok",
		Pod: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "my-ns"},
			Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{{
					Name: "proj-vol",
					VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
						Sources: []corev1.VolumeProjection{
							{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token"}},
							{ConfigMap: &corev1.ConfigMapProjection{
								LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"},
								Items:                []corev1.KeyToPath{{Key: "k", Path: "config.yaml"}},
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
			{Name: "proj-vol", MountPath: "/mnt/proj-vol"},
		}},
		ConfigMaps: map[string]*corev1.ConfigMap{
			"cfg": {Data: map[string]string{"k": "cfg-val"}},
		},
	}
}
