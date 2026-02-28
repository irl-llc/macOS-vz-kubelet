package resourcemanager

import (
	"fmt"

	"github.com/virtual-kubelet/virtual-kubelet/log"
	corev1 "k8s.io/api/core/v1"
)

// applySecurityContext maps a Kubernetes SecurityContext to container create args.
func applySecurityContext(args *ContainerCreateArgs, sc *corev1.SecurityContext) {
	if sc == nil {
		return
	}
	args.User = formatUser(sc)
	if sc.ReadOnlyRootFilesystem != nil && *sc.ReadOnlyRootFilesystem {
		args.ReadOnlyRootFS = true
	}
	applyCapabilities(args, sc.Capabilities)
	warnIgnoredSecurityFields(sc)
}

func formatUser(sc *corev1.SecurityContext) string {
	if sc.RunAsUser == nil && sc.RunAsGroup == nil {
		return ""
	}
	uid := "0"
	if sc.RunAsUser != nil {
		uid = fmt.Sprintf("%d", *sc.RunAsUser)
	}
	if sc.RunAsGroup != nil {
		return fmt.Sprintf("%s:%d", uid, *sc.RunAsGroup)
	}
	return uid
}

func warnIgnoredSecurityFields(sc *corev1.SecurityContext) {
	if sc.Privileged != nil {
		log.L.Warn("SecurityContext.Privileged is not supported")
	}
	if sc.SELinuxOptions != nil {
		log.L.Warn("SecurityContext.SELinuxOptions is not supported")
	}
	if sc.ProcMount != nil {
		log.L.Warn("SecurityContext.ProcMount is not supported")
	}
	if sc.SeccompProfile != nil {
		log.L.Warn("SecurityContext.SeccompProfile is not supported")
	}
	if sc.AppArmorProfile != nil {
		log.L.Warn("SecurityContext.AppArmorProfile is not supported")
	}
}

func applyCapabilities(args *ContainerCreateArgs, caps *corev1.Capabilities) {
	if caps == nil {
		return
	}
	for _, c := range caps.Add {
		args.CapAdd = append(args.CapAdd, string(c))
	}
	for _, c := range caps.Drop {
		args.CapDrop = append(args.CapDrop, string(c))
	}
}

// applyResources maps resource limits to container create args.
func applyResources(args *ContainerCreateArgs, res corev1.ResourceRequirements) {
	if mem, ok := res.Limits[corev1.ResourceMemory]; ok {
		args.MemoryLimitBytes = mem.Value()
	}
	if cpu, ok := res.Limits[corev1.ResourceCPU]; ok {
		args.CPULimit = cpu.AsApproximateFloat64()
	}
}
