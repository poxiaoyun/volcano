package gpumanager

import (
	v1 "k8s.io/api/core/v1"
	"k8s.io/kubernetes/pkg/kubelet/cm/topologymanager/bitmask"
	"k8s.io/utils/cpuset"

	nodeinfov1alpha1 "volcano.sh/apis/pkg/apis/nodeinfo/v1alpha1"

	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/plugins/numaaware/policy"
)

const (
	NvidiaGPUResource v1.ResourceName = "nvidia.com/gpu"
	maxNUMANodes                      = 16
)

type gpuMng struct{}

func NewProvider() policy.HintProvider {
	return &gpuMng{}
}

func (manager *gpuMng) Name() string {
	return "gpuMng"
}

func RequestedGPUs(container *v1.Container) (int, bool) {
	quantity, found := container.Resources.Requests[NvidiaGPUResource]
	if !found {
		quantity = container.Resources.Limits[NvidiaGPUResource]
	}
	return policy.IntegralQuantity(quantity)
}

func validTopology(topoInfo *api.NumatopoInfo, available cpuset.CPUSet) bool {
	if topoInfo == nil || len(topoInfo.GPUDetail) == 0 {
		return false
	}
	for gpuID, detail := range topoInfo.GPUDetail {
		if gpuID < 0 {
			return false
		}
		if _, err := bitmask.NewBitMask(detail.NUMANodeID); err != nil {
			return false
		}
	}
	if topoInfo.GPUDetail.NUMANodes().Size() > maxNUMANodes {
		return false
	}
	for _, gpuID := range available.List() {
		if _, found := topoInfo.GPUDetail[gpuID]; !found {
			return false
		}
	}
	return true
}

func (manager *gpuMng) GetTopologyHints(container *v1.Container,
	topoInfo *api.NumatopoInfo, resNumaSets api.ResNumaSets) map[string][]policy.TopologyHint {
	request, valid := RequestedGPUs(container)
	if valid && request == 0 {
		return nil
	}
	hints := []policy.TopologyHint{}
	result := map[string][]policy.TopologyHint{string(NvidiaGPUResource): hints}
	available, found := resNumaSets[string(NvidiaGPUResource)]
	if !valid || !found || available.Size() < request || !validTopology(topoInfo, available) {
		return result
	}

	numaNodes := topoInfo.GPUDetail.NUMANodes().List()
	minimum := len(numaNodes)
	bitmask.IterateBitMasks(numaNodes, func(mask bitmask.BitMask) {
		devices := topoInfo.GPUDetail.GPUsInNUMANodes(mask.GetBits()...)
		if devices.Size() >= request && mask.Count() < minimum {
			minimum = mask.Count()
		}
		if available.Intersection(devices).Size() >= request {
			hints = append(hints, policy.TopologyHint{NUMANodeAffinity: mask})
		}
	})
	for index := range hints {
		hints[index].Preferred = hints[index].NUMANodeAffinity.Count() == minimum
	}
	result[string(NvidiaGPUResource)] = hints
	return result
}

func (manager *gpuMng) Allocate(container *v1.Container, hint *policy.TopologyHint,
	topoInfo *api.NumatopoInfo, resNumaSets api.ResNumaSets) map[string]cpuset.CPUSet {
	request, valid := RequestedGPUs(container)
	available, found := resNumaSets[string(NvidiaGPUResource)]
	if !valid || request == 0 || !found || available.Size() < request {
		return nil
	}

	strict := topoInfo != nil && topoInfo.Policies[nodeinfov1alpha1.TopologyManagerPolicy] == "single-numa-node"
	validTopo := validTopology(topoInfo, available)
	if strict && (!validTopo || hint == nil || !hint.Preferred || hint.NUMANodeAffinity == nil || hint.NUMANodeAffinity.Count() != 1) {
		return nil
	}

	aligned := cpuset.New()
	if validTopo && hint != nil && hint.NUMANodeAffinity != nil {
		aligned = available.Intersection(topoInfo.GPUDetail.GPUsInNUMANodes(hint.NUMANodeAffinity.GetBits()...))
	}
	if strict && aligned.Size() < request {
		return nil
	}
	deviceIDs := aligned.List()
	if len(deviceIDs) < request {
		deviceIDs = append(deviceIDs, available.Difference(aligned).List()...)
	}
	return map[string]cpuset.CPUSet{string(NvidiaGPUResource): cpuset.New(deviceIDs[:request]...)}
}
