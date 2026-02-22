package resourcemanager

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func ptr[T any](v T) *T { return &v }

func TestApplySecurityContext_Nil(t *testing.T) {
	args := &ContainerCreateArgs{}
	applySecurityContext(args, nil)
	assert.Empty(t, args.User)
	assert.False(t, args.ReadOnlyRootFS)
}

func TestApplySecurityContext_RunAsUser(t *testing.T) {
	args := &ContainerCreateArgs{}
	sc := &corev1.SecurityContext{RunAsUser: ptr(int64(1000))}
	applySecurityContext(args, sc)
	assert.Equal(t, "1000", args.User)
}

func TestApplySecurityContext_RunAsUserAndGroup(t *testing.T) {
	args := &ContainerCreateArgs{}
	sc := &corev1.SecurityContext{
		RunAsUser:  ptr(int64(1000)),
		RunAsGroup: ptr(int64(2000)),
	}
	applySecurityContext(args, sc)
	assert.Equal(t, "1000:2000", args.User)
}

func TestApplySecurityContext_ReadOnlyRootFS(t *testing.T) {
	args := &ContainerCreateArgs{}
	sc := &corev1.SecurityContext{ReadOnlyRootFilesystem: ptr(true)}
	applySecurityContext(args, sc)
	assert.True(t, args.ReadOnlyRootFS)
}

func TestApplySecurityContext_Capabilities(t *testing.T) {
	args := &ContainerCreateArgs{}
	sc := &corev1.SecurityContext{
		Capabilities: &corev1.Capabilities{
			Add:  []corev1.Capability{"NET_ADMIN", "SYS_TIME"},
			Drop: []corev1.Capability{"ALL"},
		},
	}
	applySecurityContext(args, sc)
	assert.Equal(t, []string{"NET_ADMIN", "SYS_TIME"}, args.CapAdd)
	assert.Equal(t, []string{"ALL"}, args.CapDrop)
}

func TestApplyResources_MemoryLimit(t *testing.T) {
	args := &ContainerCreateArgs{}
	res := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		},
	}
	applyResources(args, res)
	assert.Equal(t, int64(512*1024*1024), args.MemoryLimitBytes)
	assert.InDelta(t, 0.0, args.CPULimit, 0.001)
}

func TestApplyResources_CPULimit(t *testing.T) {
	args := &ContainerCreateArgs{}
	res := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("1500m"),
		},
	}
	applyResources(args, res)
	assert.InDelta(t, 1.5, args.CPULimit, 0.001)
	assert.Equal(t, int64(0), args.MemoryLimitBytes)
}

func TestApplyResources_Empty(t *testing.T) {
	args := &ContainerCreateArgs{}
	applyResources(args, corev1.ResourceRequirements{})
	assert.Equal(t, int64(0), args.MemoryLimitBytes)
	assert.InDelta(t, 0.0, args.CPULimit, 0.001)
}

func TestFormatUser_GroupOnly(t *testing.T) {
	sc := &corev1.SecurityContext{RunAsGroup: ptr(int64(1000))}
	assert.Equal(t, "0:1000", formatUser(sc))
}

func TestFormatUser_NeitherUserNorGroup(t *testing.T) {
	sc := &corev1.SecurityContext{}
	assert.Empty(t, formatUser(sc))
}
