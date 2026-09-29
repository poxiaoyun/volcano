# CPU and NVIDIA GPU NUMA alignment

This backport targets Volcano v1.15.1 and resource-exporter v1.0.0. It supports
exclusive physical GPUs requested as `nvidia.com/gpu`. It does not add support for
`volcano.sh/vgpu-*`, GPU sharing, MIG, or memory NUMA placement.

## Scheduling contract

The exporter reports available GPU devices and their NUMA affinity in
`Numatopology.spec.gpuDetail` and `spec.numares["nvidia.com/gpu"]`. The scheduler
merges CPU and GPU hints for each container. With `single-numa-node`, a container
must have enough exclusive CPUs and GPUs on the same NUMA node; missing GPU
topology is not treated as an unconstrained placement. Node scoring counts the
union of CPU and GPU NUMA nodes rather than adding their counts independently.

The scheduler's device IDs are an estimate used for feasibility and reservation,
not a device-selection command to the NVIDIA plugin. **Kubelet performs the final
CPU and GPU selection and enforces the topology policy.** Its selected NUMA node
or device IDs may differ from the scheduler's estimate. Concurrent allocations,
stale exporter data, and asynchronous binding can still cause kubelet admission
failure. A workload controller must replace a rejected Pod. This change does not
add a persistent per-Pod reservation/acknowledgement protocol to Volcano v1.15.1.
Assignments are reserved between exporter updates. A new authoritative report
replaces those estimates so GPUs actually selected or released by kubelet cannot
remain permanently hidden behind an obsolete scheduler device choice.

## Prerequisites

Configure kubelet on eligible GPU nodes:

```yaml
cpuManagerPolicy: static
topologyManagerPolicy: single-numa-node
topologyManagerScope: container
```

Keep the node's existing CPU reservations configured. Apply kubelet configuration
changes using the normal node maintenance procedure; do not delete CPU manager
state while workloads are running.

Install the NVIDIA device plugin with native `nvidia.com/gpu` resources and
physical-device topology reporting. Disable time slicing, MPS and MIG for this
path. Do not advertise the same physical GPUs through both native and Volcano
vGPU device plugins. The exporter requires the kubelet PodResources `List` and
`GetAllocatableResources` APIs through its existing socket mount, plus the kubelet
device-plugin checkpoint. Do not enable the device plugin option that omits NUMA
topology reporting.
Upgrade the exporter's RBAC as well: node-local Pod liveness reconciliation
requires `list` access to Pods, so stale checkpoint entries do not block GPU reuse.

Run the exporter with `--enable-pod-resource=false` to collect CPU allocations
from `cpu_manager_state`. GPU collection still uses PodResources. This avoids
advertising CPUs held by ordinary init containers, which the kubelet PodResources
`List` response does not include. GPU allocation accounting additionally reads
`kubelet_internal_checkpoint` for the same reason. For the supplied manifest's
mount paths, use:

```text
--enable-pod-resource=false
--cpu-manager-state=/host/kubelet/cpu_manager_state
--device-plugin-checkpoint=/host/kubelet/device-plugins/kubelet_internal_checkpoint
```

The supported strict scheduling scope is **per container**, not the entire Pod.
Different containers in one Pod may use different NUMA nodes. GPU Pods with init
containers are rejected in strict mode because this backport does not model
their resource reuse. Do not change kubelet to Pod scope and assume the scheduler
has Pod-level topology support.

## Build and deploy

Build custom scheduler and exporter images from the modified repositories using
their existing build workflows. Use new image tags; the upstream `v1.15.1` and
`v1.0.0` images do not contain this backport. The exporter pins the API revision
used by resource-exporter PR #12; see its README for the dependency details.

Upgrade the CRD **before** deploying the custom binaries:

```bash
kubectl apply -f config/crd/volcano/bases/nodeinfo.volcano.sh_numatopologies.yaml
```

Run that command from the Volcano repository. Helm upgrades do not automatically
upgrade existing CRDs. The new `gpuDetail` schema is necessary to prevent the API
server from pruning the reported topology. Deploy the custom scheduler image and
the custom exporter image on eligible GPU nodes, retaining the existing kubelet
configuration, CPU topology and PodResources socket mounts, and the read-only
kubelet state directory containing both checkpoint files.

Enable `numa-aware` in the scheduler's existing plugin tier, for example:

```yaml
actions: "enqueue, allocate, backfill"
tiers:
  - plugins:
      - name: priority
      - name: gang
      - name: conformance
  - plugins:
      - name: drf
      - name: predicates
      - name: proportion
      - name: nodeorder
      - name: numa-aware
```

Use this as a minimal example, not as a replacement for an existing cluster's
queue or device configuration. No `deviceshare.VGPUEnable` setting is needed for
native `nvidia.com/gpu` requests.

## Example Pod

Every GPU container must have a positive integral CPU request. All containers
must satisfy Guaranteed QoS (CPU and memory requests equal limits).

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: cpu-gpu-same-numa
  annotations:
    volcano.sh/numa-topology-policy: single-numa-node
spec:
  schedulerName: volcano
  restartPolicy: Never
  containers:
    - name: worker
      image: nvidia/cuda:12.4.1-base-ubuntu22.04
      command: ["sh", "-c", "nvidia-smi -L; sleep infinity"]
      resources:
        requests:
          cpu: "4"
          memory: 8Gi
          nvidia.com/gpu: "1"
        limits:
          cpu: "4"
          memory: 8Gi
          nvidia.com/gpu: "1"
```

`best-effort` is only a locality preference and can allow cross-NUMA placement.
`restricted` can also allow more than one NUMA node. Use `single-numa-node` for
the strict requirement.

## Acceptance checks

1. Inspect `kubectl get numatopo <node> -o yaml`. Verify CPU topology,
   `gpuDetail`, and `numares["nvidia.com/gpu"]` are present. GPU indices are
   exporter-local stable identifiers, not CUDA/NVML ordinals.
2. Run the example. Inspect its actual CPU set via
   `kubectl exec cpu-gpu-same-numa -- grep Cpus_allowed_list /proc/self/status`
   and its GPU UUID/PCI bus with
   `kubectl exec cpu-gpu-same-numa -- nvidia-smi --query-gpu=uuid,pci.bus_id --format=csv,noheader`.
3. On the host, map those CPUs to `/sys/devices/system/node/node*/cpulist` and
   the assigned GPU PCI address to `/sys/bus/pci/devices/<BDF>/numa_node`.
   Confirm they identify the same NUMA node. Normalize the PCI domain to the
   sysfs BDF format when comparing NVIDIA output.
4. Occupy the local CPUs or GPUs so no NUMA node can satisfy both requests.
   A new strict Pod must remain Pending rather than select a cross-NUMA node.
5. Remove GPU topology or stop PodResources reporting, then verify subsequent
   exporter updates no longer advertise free GPUs for strict scheduling.
6. Release workloads and verify GPU availability returns before rescheduling.

Unit tests do not replace these real-hardware checks.

## References

- [Volcano PR #5095](https://github.com/volcano-sh/volcano/pull/5095)
- [APIs PR #229](https://github.com/volcano-sh/apis/pull/229)
- [resource-exporter PR #12](https://github.com/volcano-sh/resource-exporter/pull/12)
- [Kubernetes Topology Manager](https://kubernetes.io/docs/tasks/administer-cluster/topology-manager/)
