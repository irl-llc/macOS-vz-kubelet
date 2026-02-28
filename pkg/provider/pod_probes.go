package provider

import (
	"context"

	"github.com/agoda-com/macOS-vz-kubelet/internal/node"
	"github.com/agoda-com/macOS-vz-kubelet/pkg/resource"
)

// execProbe runs a command in a container, satisfying the probes.ExecRunner signature.
func (p *MacOSVZProvider) execProbe(ctx context.Context, ns, pod, container string, cmd []string, attach *node.ExecIO) error {
	return p.vzClient.ExecuteContainerCommand(ctx, ns, pod, container, cmd, attach)
}

// resolveContainerIP returns the IP for a specific container in a pod.
// VM containers use the VM IP; native containers use their vmnet IP;
// falls back to the host IP when no network address is available.
func (p *MacOSVZProvider) resolveContainerIP(ctx context.Context, ns, pod, container string) (string, error) {
	vg, err := p.vzClient.GetVirtualizationGroup(ctx, ns, pod)
	if err != nil {
		return "", err
	}
	if vg.HasVM() {
		return vg.MacOSVirtualMachine.IPAddress(), nil
	}
	if ip := containerIP(vg.Containers, container); ip != "" {
		return ip, nil
	}
	return p.nodeIPAddress, nil
}

func containerIP(containers []resource.Container, name string) string {
	for _, c := range containers {
		if c.Name == name {
			return c.IPAddress
		}
	}
	return ""
}
