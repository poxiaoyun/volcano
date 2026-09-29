# Native GPU NUMA placement

The GPU provider handles only `nvidia.com/gpu`, not vGPU or MIG resources.
CPU and GPU assignments contribute the union of their NUMA nodes to scoring.

For `single-numa-node`, GPU workloads must be Guaranteed pods, and every GPU
container must request positive integral CPUs. Missing or invalid CPU/GPU
topology, unknown or exhausted GPU resources, and incompatible CPU/GPU hints
reject placement. Allocations never spill outside the selected NUMA node.
Containers reserve distinct CPU/GPU sets within each predicate evaluation;
alignment is per container, not across the entire pod.

Strict GPU pods containing any init containers, including restartable init
containers, are currently rejected. Init-container resource reuse and sidecar
lifetimes are not implemented.

Best-effort policy can fall back to cross-NUMA allocation or missing topology.
Known GPU availability must still satisfy the complete request; partial GPU
assignments are never returned. Unknown GPU resources yield non-nil empty
hints rather than a no-preference hint; best-effort admission then leaves the
scalar resource check to the scheduler's resource predicates.

Device IDs here are scheduler estimates. Native NVIDIA device allocation still
belongs to kubelet; matching kubelet topology-manager policy and container scope
are required for enforcement. Concurrent changes can still cause kubelet
admission failure.
