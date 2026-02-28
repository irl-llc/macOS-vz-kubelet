package resourcemanager

import (
	"context"
	"io"

	"github.com/agoda-com/macOS-vz-kubelet/pkg/resource"
	"github.com/virtual-kubelet/virtual-kubelet/node/api"
)

// CLIExecutor abstracts the container CLI binary for testability.
type CLIExecutor interface {
	// ListContainers returns IDs of containers whose names match a prefix.
	ListContainers(ctx context.Context, namePrefix string) ([]string, error)

	// CreateAndStartContainer creates and starts a container, returning its ID.
	CreateAndStartContainer(ctx context.Context, args ContainerCreateArgs) (string, error)

	// RemoveContainer removes a container by ID, optionally forcing removal.
	RemoveContainer(ctx context.Context, id string, force bool) error

	// InspectContainer returns the current state of a container.
	InspectContainer(ctx context.Context, id string) (resource.ContainerState, error)

	// ContainerLogs returns a reader for the container's log output.
	ContainerLogs(ctx context.Context, name string, opts api.ContainerLogOpts) (io.ReadCloser, error)

	// ExecInContainer runs a command inside a running container.
	ExecInContainer(ctx context.Context, name string, cmd []string, attach api.AttachIO) error

	// AttachToContainer connects to a running container's I/O streams.
	AttachToContainer(ctx context.Context, name string, attach api.AttachIO) error

	// PullImage fetches a container image from a registry.
	PullImage(ctx context.Context, image string, creds resource.RegistryCredentials) error

	// RemoveImage deletes a container image from local storage.
	RemoveImage(ctx context.Context, image string) error

	// ContainerStats returns CPU (nanoseconds) and memory (bytes) usage.
	ContainerStats(ctx context.Context, id string) (cpuNano uint64, memBytes uint64, err error)
}

// ContainerCreateArgs holds parameters for creating a container via CLI.
type ContainerCreateArgs struct {
	Name       string
	Image      string
	Env        []string // KEY=VALUE format
	Binds      []string // host:container:mode format
	Command    []string
	Args       []string
	WorkingDir string
	TTY        bool
	Stdin      bool

	// Security
	User           string // uid:gid
	ReadOnlyRootFS bool
	CapAdd         []string
	CapDrop        []string

	// Resources
	MemoryLimitBytes int64
	CPULimit         float64 // number of CPUs (fractional)
}
