package provider

import (
	"context"

	"github.com/agoda-com/macOS-vz-kubelet/internal/node"
)

// execProbe runs a command in a container, satisfying the probes.ExecRunner signature.
func (p *MacOSVZProvider) execProbe(ctx context.Context, ns, pod, container string, cmd []string, attach *node.ExecIO) error {
	return p.vzClient.ExecuteContainerCommand(ctx, ns, pod, container, cmd, attach)
}

// resolveContainerIP returns the IP for a container's pod.
// For VM containers this is the VM IP; for native containers we return the host IP.
func (p *MacOSVZProvider) resolveContainerIP(ctx context.Context, ns, pod, container string) (string, error) {
	vg, err := p.vzClient.GetVirtualizationGroup(ctx, ns, pod)
	if err != nil {
		return "", err
	}
	if vg.HasVM() {
		return vg.MacOSVirtualMachine.IPAddress(), nil
	}
	return p.nodeIPAddress, nil
}
