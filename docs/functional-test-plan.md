# Functional Test Plan

This document covers manual and automated validation for the macOS-vz-kubelet feature set.

## 1. Volume Tests

- [ ] Pod with ConfigMap volume: verify files appear in container with correct content
- [ ] Pod with Secret volume: verify files appear with correct permissions (0644 default)
- [ ] Pod with Projected volume: verify SA token, configmap, and downwardAPI all merged
- [ ] Pod with PVC (hostPath provisioner): verify data persists across pod restarts
- [ ] Pod with EmptyDir: verify shared storage between sidecar and main container
- [ ] Pod with downwardAPI volume: verify metadata.name, metadata.namespace, metadata.uid, metadata.labels, metadata.annotations, resource limits/requests

## 2. Container Runtime Tests

- [ ] Pod with only native Linux containers (no VM): verify full lifecycle (create, run, terminate)
- [ ] Pod with macOS VM + native Linux sidecars: verify mixed runtime coexistence
- [ ] Pod with only macOS VM: verify backwards compatibility with pre-refactor behavior
- [ ] Container exec into native container: verify command execution and exit code
- [ ] Container logs from native container: verify stdout/stderr streaming
- [ ] Container attach to native container: verify interactive I/O
- [ ] Image pull with PullAlways policy: verify image is re-pulled
- [ ] Image pull with PullNever policy: verify no pull occurs
- [ ] Image pull with registry credentials: verify authenticated pull succeeds

## 3. Init Container Tests

- [ ] Pod with init containers that all succeed: main containers start
- [ ] Pod with init container that fails (non-zero exit): pod enters Failed phase
- [ ] Init container with volume mounts: data written by init container is available to main containers
- [ ] Multiple init containers: verify sequential execution order
- [ ] Init container cleanup: verify init containers are removed from runtime after completion

## 4. Health Check Tests

- [ ] Liveness probe (exec): container restarted on repeated failure
- [ ] Readiness probe (HTTP GET): pod not Ready until probe passes, Ready once passing
- [ ] Startup probe: gates liveness/readiness probes until passing
- [ ] TCP probe: verifies port connectivity
- [ ] Probe with custom thresholds: successThreshold=3 requires 3 consecutive passes
- [ ] Probe with initialDelaySeconds: probe does not run before delay expires
- [ ] Probe timeout: slow probe is treated as failure

## 5. Pod Update Tests

- [ ] Update pod labels/annotations: reflected in subsequent GetPodStatus calls
- [ ] Probe reconfiguration on update: probes restart with new configuration

## 6. Security Tests

- [ ] RunAsUser/RunAsGroup enforced: container process runs as specified UID/GID
- [ ] ReadOnlyRootFilesystem enforced: write to root filesystem fails
- [ ] Capability drop ALL: container cannot perform privileged operations
- [ ] Capability add NET_ADMIN: container can modify network settings

## 7. Resource Limits Tests

- [ ] Container memory limit applied: OOMKilled when exceeding limit
- [ ] Container CPU limit applied: throttling observed under load

## 8. Metrics Tests

- [ ] Container stats endpoint returns CPU and memory usage for native containers
- [ ] VM stats collected via SSH for macOS VM containers
- [ ] Mixed pod: both VM and container stats returned correctly

## 9. Networking Tests

### Pod IP Assignment

- [ ] Container-only pod (2+ containers): pod reports non-empty PodIP from first container's vmnet address
- [ ] Container-only pod (single container): pod reports that container's vmnet IP as PodIP
- [ ] VM-only pod: pod reports VM's IP as PodIP (existing behavior preserved)
- [ ] Mixed pod (VM + sidecars): pod reports VM IP as PodIP (sidecars get separate vmnet IPs)

### Per-Pod Network Lifecycle

- [ ] Pod creation creates a vmnet network named `vk-<uid[:12]>`; verify with `container network ls`
- [ ] Pod deletion removes the vmnet network; verify with `container network ls`
- [ ] All containers in a pod are attached to the same named network
- [ ] Network cleanup occurs even if container removal fails

### Inter-Container Communication

- [ ] Two containers in the same pod can ping each other via vmnet IPs
- [ ] Two containers in the same pod can open TCP connections to each other
- [ ] Containers in different pods cannot reach each other's pod-internal network (network isolation)

### DNS Injection

- [ ] With `CLUSTER_DNS=10.96.0.10`: container's `/etc/resolv.conf` contains `nameserver 10.96.0.10`
- [ ] With `CLUSTER_DNS` unset: no DNS override applied (runtime default used)
- [ ] Pod with `dnsPolicy: None` and explicit `dnsConfig.nameservers`: uses pod-specified nameservers
- [ ] Pod with `dnsPolicy: Default`: no cluster DNS injected
- [ ] With cluster DNS configured: `nslookup kubernetes.default` resolves inside container

### Port Forwarding

- [ ] `kubectl port-forward pod/<name> 8080:80`: TCP traffic proxied to pod IP on port 80
- [ ] Port forward to container-only pod: resolves pod IP from container vmnet address
- [ ] Port forward to VM pod: resolves pod IP from VM address
- [ ] Port forward to pod with no IP: returns clear error message
- [ ] Multiple concurrent port-forward sessions to same pod: all function independently

### Known Limitations

- [ ] Containers cannot reach each other via `localhost` (each has its own VM network stack)
- [ ] No static IP assignment (IPs assigned by DHCP on vmnet)

## 10. Pod Lifecycle Tests

- [ ] Pod deletion with gracePeriod=0: immediate cleanup
- [ ] Pod deletion with gracePeriod>0 and preStop hooks: hooks execute before cleanup
- [ ] PreStop hook failure: pod deletion proceeds after grace period
- [ ] Failed pod auto-cleanup: completed/failed pods cleaned from provider
- [ ] Runtime annotation routing: `vz.kubelet.io/vm-containers` correctly designates VM containers

## Running Tests

### Unit Tests
```bash
go test ./...
```

### Integration Tests (E2E)
```bash
go test ./e2e_test/... -v -namespace <namespace> -node-name <node-name> -macos-image <image-ref>
```

### Manual Smoke Tests

Each test above can be executed manually by creating a pod YAML, applying it via `kubectl`, and verifying the expected behavior. Example:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: probe-test
  annotations:
    vz.kubelet.io/vm-containers: ""
spec:
  nodeSelector:
    kubernetes.io/role: agent
    type: virtual-kubelet
  tolerations:
  - key: virtual-kubelet.io/provider
    operator: Exists
  containers:
  - name: web
    image: nginx:latest
    readinessProbe:
      httpGet:
        path: /
        port: 80
      periodSeconds: 5
    livenessProbe:
      tcpSocket:
        port: 80
      periodSeconds: 10
```

Networking smoke test (two containers sharing a pod network):

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: network-test
  annotations:
    vz.kubelet.io/vm-containers: ""
spec:
  dnsPolicy: ClusterFirst
  nodeSelector:
    kubernetes.io/role: agent
    type: virtual-kubelet
  tolerations:
  - key: virtual-kubelet.io/provider
    operator: Exists
  containers:
  - name: web
    image: nginx:latest
  - name: client
    image: alpine:latest
    command: ["sleep", "3600"]
```

After applying, verify:
```bash
# Pod has an IP
kubectl get pod network-test -o jsonpath='{.status.podIP}'

# Containers can reach each other (get web container's IP first)
kubectl exec network-test -c client -- wget -qO- http://<web-container-ip>

# Port forwarding works
kubectl port-forward pod/network-test 8080:80 &
curl http://localhost:8080

# DNS resolves (requires CLUSTER_DNS configured)
kubectl exec network-test -c client -- nslookup kubernetes.default
```

## 11. Error and Failure Scenarios

- [ ] Pod referencing missing ConfigMap (non-optional): verify pod enters Failed phase with clear error message
- [ ] Pod referencing missing Secret (non-optional): verify pod enters Failed phase
- [ ] Pod with invalid SecurityContext field (e.g., Privileged=true): verify warning logged, pod still starts
- [ ] Init container timeout (ActiveDeadlineSeconds exceeded): verify pod eventually fails
- [ ] Container with invalid image reference: verify pull failure reported via events
- [ ] Duplicate container creation: verify second create returns "already exists" error
