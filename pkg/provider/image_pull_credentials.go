package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/agoda-com/macOS-vz-kubelet/pkg/resource"
	"github.com/virtual-kubelet/virtual-kubelet/log"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	credentialprovider "k8s.io/kubernetes/pkg/credentialprovider"
)

var (
	errEmptyDockerConfig = errors.New("docker config has no auth entries")
)

func (p *MacOSVZProvider) resolveImagePullCredentials(ctx context.Context, pod *corev1.Pod) (resource.RegistryCredentialStore, error) {
	keyring := &credentialprovider.BasicDockerKeyring{}
	hasCredentials := false
	seen := make(map[string]struct{})
	var secretRefs []corev1.LocalObjectReference

	for _, ref := range pod.Spec.ImagePullSecrets {
		if _, ok := seen[ref.Name]; ok {
			continue
		}
		seen[ref.Name] = struct{}{}
		secretRefs = append(secretRefs, ref)
	}

	saName := pod.Spec.ServiceAccountName
	if saName == "" {
		saName = "default"
	}

	sa, err := p.k8sClient.CoreV1().ServiceAccounts(pod.Namespace).Get(ctx, saName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return resource.RegistryCredentialStore{}, fmt.Errorf("service account %q not found: %w", saName, err)
	case err != nil:
		return resource.RegistryCredentialStore{}, fmt.Errorf("failed to fetch service account %q: %w", saName, err)
	default:
		// continue with resolved service account
	}

	for _, ref := range sa.ImagePullSecrets {
		if _, ok := seen[ref.Name]; ok {
			continue
		}
		seen[ref.Name] = struct{}{}
		secretRefs = append(secretRefs, ref)
	}

	for _, ref := range secretRefs {
		secret, err := p.k8sClient.CoreV1().Secrets(pod.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return resource.RegistryCredentialStore{}, fmt.Errorf("imagePullSecret %q not found: %w", ref.Name, err)
			}
			return resource.RegistryCredentialStore{}, fmt.Errorf("failed to fetch imagePullSecret %q: %w", ref.Name, err)
		}

		switch secret.Type {
		case corev1.SecretTypeDockerConfigJson, corev1.SecretTypeDockercfg:
			// supported types
		default:
			warnErr := fmt.Errorf("ignoring imagePullSecret %q: unsupported type %q", ref.Name, secret.Type)
			log.G(ctx).WithError(warnErr).Warn("Unsupported imagePullSecret type")
			p.eventRecorder.FailedToResolveImagePullSecrets(ctx, warnErr)
			continue
		}

		dockerConfig, err := parseImagePullSecret(secret)
		if err != nil {
			return resource.RegistryCredentialStore{}, fmt.Errorf("failed to parse imagePullSecret %q: %w", ref.Name, err)
		}

		keyring.Add(&credentialprovider.CredentialSource{
			Secret: &credentialprovider.SecretCoordinates{
				UID:       string(secret.UID),
				Namespace: secret.Namespace,
				Name:      secret.Name,
			},
		}, dockerConfig)
		hasCredentials = true
	}

	if !hasCredentials {
		return resource.RegistryCredentialStore{}, nil
	}

	return resource.NewRegistryCredentialStore(keyring), nil
}

func parseImagePullSecret(secret *corev1.Secret) (credentialprovider.DockerConfig, error) {
	switch secret.Type {
	case corev1.SecretTypeDockerConfigJson:
		return parseDockerConfigJSON(secret.Data[corev1.DockerConfigJsonKey])
	case corev1.SecretTypeDockercfg:
		if data, ok := secret.Data[corev1.DockerConfigJsonKey]; ok && len(data) > 0 {
			return parseDockerConfigJSON(data)
		}
		return parseDockerConfig(secret.Data[corev1.DockerConfigKey])
	default:
		return nil, fmt.Errorf("unsupported secret type %q", secret.Type)
	}
}

func parseDockerConfigJSON(raw []byte) (credentialprovider.DockerConfig, error) {
	if len(raw) == 0 {
		return nil, errEmptyDockerConfig
	}

	var cfg credentialprovider.DockerConfigJSON
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("invalid docker config json: %w", err)
	}

	if len(cfg.Auths) == 0 {
		return nil, errEmptyDockerConfig
	}

	return cfg.Auths, nil
}

func parseDockerConfig(raw []byte) (credentialprovider.DockerConfig, error) {
	if len(raw) == 0 {
		return nil, errEmptyDockerConfig
	}

	var cfg credentialprovider.DockerConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("invalid dockercfg: %w", err)
	}

	if len(cfg) == 0 {
		return nil, errEmptyDockerConfig
	}

	return cfg, nil
}
