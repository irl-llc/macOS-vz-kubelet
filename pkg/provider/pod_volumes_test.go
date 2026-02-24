package provider

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func boolRef(b bool) *bool { return &b }

func newTestProvider(t *testing.T, k8sClient *fake.Clientset) *MacOSVZProvider {
	t.Helper()
	return &MacOSVZProvider{k8sClient: k8sClient}
}

func TestExtractPodVolumeData_Token(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()

	_, err := client.CoreV1().ServiceAccounts("ns").Create(ctx, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: "sa", Namespace: "ns"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	client.PrependReactor("create", "serviceaccounts", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, &authv1.TokenRequest{
			Status: authv1.TokenRequestStatus{Token: "tok-123"},
		}, nil
	})

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "ns"},
		Spec: corev1.PodSpec{
			ServiceAccountName: "sa",
			Volumes: []corev1.Volume{
				{
					Name: "sa-proj",
					VolumeSource: corev1.VolumeSource{
						Projected: &corev1.ProjectedVolumeSource{
							Sources: []corev1.VolumeProjection{
								{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token"}},
							},
						},
					},
				},
			},
		},
	}

	p := newTestProvider(t, client)
	data, err := p.extractPodVolumeData(ctx, pod)
	require.NoError(t, err)
	assert.Equal(t, "tok-123", data.ServiceAccountToken)
}

func TestExtractPodVolumeData_ConfigMaps(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "my-cm", Namespace: "ns"},
		Data:       map[string]string{"k": "v"},
	}
	_, err := client.CoreV1().ConfigMaps("ns").Create(ctx, cm, metav1.CreateOptions{})
	require.NoError(t, err)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "ns"},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: boolRef(false),
			Volumes: []corev1.Volume{
				{
					Name: "cm-vol",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: "my-cm"},
						},
					},
				},
			},
		},
	}

	p := newTestProvider(t, client)
	data, err := p.extractPodVolumeData(ctx, pod)
	require.NoError(t, err)
	require.Contains(t, data.ConfigMaps, "my-cm")
	assert.Equal(t, "v", data.ConfigMaps["my-cm"].Data["k"])
}

func TestExtractPodVolumeData_Secrets(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "db-sec", Namespace: "ns"},
		Data:       map[string][]byte{"pw": []byte("s3cret")},
	}
	_, err := client.CoreV1().Secrets("ns").Create(ctx, secret, metav1.CreateOptions{})
	require.NoError(t, err)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "ns"},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: boolRef(false),
			Volumes: []corev1.Volume{
				{
					Name: "sec-vol",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{SecretName: "db-sec"},
					},
				},
			},
		},
	}

	p := newTestProvider(t, client)
	data, err := p.extractPodVolumeData(ctx, pod)
	require.NoError(t, err)
	require.Contains(t, data.Secrets, "db-sec")
	assert.Equal(t, []byte("s3cret"), data.Secrets["db-sec"].Data["pw"])
}

func TestExtractPodVolumeData_OptionalConfigMapMissing(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "ns"},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: boolRef(false),
			Volumes: []corev1.Volume{
				{
					Name: "opt",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: "gone"},
							Optional:             boolRef(true),
						},
					},
				},
			},
		},
	}

	p := newTestProvider(t, client)
	data, err := p.extractPodVolumeData(ctx, pod)
	require.NoError(t, err)
	assert.Empty(t, data.ConfigMaps)
}

func TestExtractPodVolumeData_RequiredSecretMissing(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "ns"},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: boolRef(false),
			Volumes: []corev1.Volume{
				{
					Name: "req",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{SecretName: "absent"},
					},
				},
			},
		},
	}

	p := newTestProvider(t, client)
	_, err := p.extractPodVolumeData(ctx, pod)
	assert.Error(t, err)
}

func TestExtractPodVolumeData_ProjectedSecret(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "proj-sec", Namespace: "ns"},
		Data:       map[string][]byte{"cert": []byte("pem-data")},
	}
	_, err := client.CoreV1().Secrets("ns").Create(ctx, secret, metav1.CreateOptions{})
	require.NoError(t, err)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "ns"},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: boolRef(false),
			Volumes: []corev1.Volume{
				{
					Name: "proj-vol",
					VolumeSource: corev1.VolumeSource{
						Projected: &corev1.ProjectedVolumeSource{
							Sources: []corev1.VolumeProjection{
								{Secret: &corev1.SecretProjection{
									LocalObjectReference: corev1.LocalObjectReference{Name: "proj-sec"},
								}},
							},
						},
					},
				},
			},
		},
	}

	p := newTestProvider(t, client)
	data, err := p.extractPodVolumeData(ctx, pod)
	require.NoError(t, err)
	require.Contains(t, data.Secrets, "proj-sec")
	assert.Equal(t, []byte("pem-data"), data.Secrets["proj-sec"].Data["cert"])
}
