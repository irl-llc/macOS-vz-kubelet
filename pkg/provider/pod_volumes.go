package provider

import (
	"context"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/agoda-com/macOS-vz-kubelet/internal/volumes"

	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// extractPodVolumeData gathers service account tokens, config maps, secrets,
// and PVC resolutions that the pod's volumes reference.
func (p *MacOSVZProvider) extractPodVolumeData(ctx context.Context, pod *corev1.Pod) (*volumes.PodVolumeData, error) {
	data := &volumes.PodVolumeData{
		ConfigMaps: make(map[string]*corev1.ConfigMap),
		Secrets:    make(map[string]*corev1.Secret),
		PVCs:       make(map[string]*volumes.PVCResolution),
	}

	// cmMu protects data.ConfigMaps written by two goroutines concurrently.
	var cmMu sync.Mutex
	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error { return p.populateServiceAccountData(ctx, pod, data, &cmMu) })
	g.Go(func() error { return p.populateStandaloneConfigMaps(ctx, pod, data, &cmMu) })
	g.Go(func() error { return p.populateSecrets(ctx, pod, data) })
	g.Go(func() error { return p.populatePVCs(ctx, pod, data) })

	if err := g.Wait(); err != nil {
		return nil, err
	}
	return data, nil
}

func (p *MacOSVZProvider) populateServiceAccountData(
	ctx context.Context, pod *corev1.Pod, data *volumes.PodVolumeData, cmMu *sync.Mutex,
) error {
	automount := pod.Spec.AutomountServiceAccountToken == nil ||
		*pod.Spec.AutomountServiceAccountToken
	if !automount {
		return nil
	}

	for _, cmProj := range findProjectedConfigMaps(pod) {
		if err := p.fetchConfigMap(ctx, cmMu, pod.Namespace, cmProj.Name, data.ConfigMaps); err != nil {
			return err
		}
	}

	return p.fetchServiceAccountToken(ctx, pod, data)
}

func (p *MacOSVZProvider) fetchServiceAccountToken(
	ctx context.Context, pod *corev1.Pod, data *volumes.PodVolumeData,
) error {
	svcProj := findSATokenProjection(pod)
	if svcProj == nil {
		return nil
	}
	token, err := p.createServiceAccountToken(
		ctx, pod.Namespace, pod.Spec.ServiceAccountName, svcProj,
	)
	if err != nil {
		return err
	}
	data.ServiceAccountToken = token
	return nil
}

// populateStandaloneConfigMaps fetches ConfigMaps referenced by standalone ConfigMap volumes.
func (p *MacOSVZProvider) populateStandaloneConfigMaps(
	ctx context.Context, pod *corev1.Pod, data *volumes.PodVolumeData, cmMu *sync.Mutex,
) error {
	for _, vol := range pod.Spec.Volumes {
		if vol.ConfigMap == nil {
			continue
		}
		err := p.fetchConfigMap(ctx, cmMu, pod.Namespace, vol.ConfigMap.Name, data.ConfigMaps)
		if err == nil {
			continue
		}
		isOptional := vol.ConfigMap.Optional != nil && *vol.ConfigMap.Optional
		if isOptional {
			continue
		}
		return err
	}
	return nil
}

// populateSecrets fetches Secrets referenced by Secret volumes and projected Secret sources.
func (p *MacOSVZProvider) populateSecrets(
	ctx context.Context, pod *corev1.Pod, data *volumes.PodVolumeData,
) error {
	for _, vol := range pod.Spec.Volumes {
		if err := p.fetchSecretVolume(ctx, pod.Namespace, vol, data); err != nil {
			return err
		}
		if err := p.fetchProjectedSecrets(ctx, pod.Namespace, vol, data); err != nil {
			return err
		}
	}
	return nil
}

func (p *MacOSVZProvider) fetchSecretVolume(
	ctx context.Context, namespace string, vol corev1.Volume, data *volumes.PodVolumeData,
) error {
	if vol.Secret == nil {
		return nil
	}
	err := p.fetchSecret(ctx, namespace, vol.Secret.SecretName, data.Secrets)
	if err == nil {
		return nil
	}
	isOptional := vol.Secret.Optional != nil && *vol.Secret.Optional
	if isOptional {
		return nil
	}
	return err
}

func (p *MacOSVZProvider) fetchProjectedSecrets(
	ctx context.Context, namespace string, vol corev1.Volume, data *volumes.PodVolumeData,
) error {
	if vol.Projected == nil {
		return nil
	}
	for _, source := range vol.Projected.Sources {
		if source.Secret == nil {
			continue
		}
		err := p.fetchSecret(ctx, namespace, source.Secret.Name, data.Secrets)
		if err == nil {
			continue
		}
		isOptional := source.Secret.Optional != nil && *source.Secret.Optional
		if isOptional {
			continue
		}
		return err
	}
	return nil
}

// populatePVCs resolves PVC volumes via the K8s API.
func (p *MacOSVZProvider) populatePVCs(
	ctx context.Context, pod *corev1.Pod, data *volumes.PodVolumeData,
) error {
	pvcs, err := volumes.ResolvePVCs(ctx, p.k8sClient, pod)
	if err != nil {
		return err
	}
	data.PVCs = pvcs
	return nil
}

func (p *MacOSVZProvider) fetchConfigMap(
	ctx context.Context, mu *sync.Mutex, namespace, name string, dest map[string]*corev1.ConfigMap,
) error {
	mu.Lock()
	_, exists := dest[name]
	mu.Unlock()
	if exists {
		return nil
	}
	cm, err := p.k8sClient.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	mu.Lock()
	dest[name] = cm
	mu.Unlock()
	return nil
}

func (p *MacOSVZProvider) fetchSecret(
	ctx context.Context, namespace, name string, dest map[string]*corev1.Secret,
) error {
	if _, ok := dest[name]; ok {
		return nil
	}
	secret, err := p.k8sClient.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	dest[name] = secret
	return nil
}

// createServiceAccountToken creates a token for the service account.
func (p *MacOSVZProvider) createServiceAccountToken(
	ctx context.Context, namespace, saName string, svcProj *corev1.ServiceAccountTokenProjection,
) (string, error) {
	var audiences []string
	if svcProj.Audience != "" {
		audiences = []string{svcProj.Audience}
	}
	tokenReq := &authv1.TokenRequest{
		Spec: authv1.TokenRequestSpec{
			Audiences:         audiences,
			ExpirationSeconds: svcProj.ExpirationSeconds,
		},
	}
	req, err := p.k8sClient.CoreV1().ServiceAccounts(namespace).CreateToken(
		ctx, saName, tokenReq, metav1.CreateOptions{},
	)
	if err != nil {
		return "", err
	}
	// TODO: ideally implement token rotation
	return req.Status.Token, nil
}

// findSATokenProjection returns the first ServiceAccountTokenProjection from projected volumes.
func findSATokenProjection(pod *corev1.Pod) *corev1.ServiceAccountTokenProjection {
	for _, vol := range pod.Spec.Volumes {
		if vol.Projected == nil {
			continue
		}
		for _, source := range vol.Projected.Sources {
			if source.ServiceAccountToken != nil {
				return source.ServiceAccountToken
			}
		}
	}
	return nil
}

// findProjectedConfigMaps returns all ConfigMapProjections from projected volumes.
func findProjectedConfigMaps(pod *corev1.Pod) []*corev1.ConfigMapProjection {
	var projs []*corev1.ConfigMapProjection
	for _, vol := range pod.Spec.Volumes {
		if vol.Projected == nil {
			continue
		}
		for _, source := range vol.Projected.Sources {
			if source.ConfigMap != nil {
				projs = append(projs, source.ConfigMap)
			}
		}
	}
	return projs
}
