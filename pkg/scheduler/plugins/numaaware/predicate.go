package numaaware

import (
	"fmt"

	v1 "k8s.io/api/core/v1"
	v1qos "k8s.io/kubernetes/pkg/apis/core/v1/helper/qos"
	"k8s.io/kubernetes/pkg/kubelet/cm/topologymanager/bitmask"

	nodeinfov1alpha1 "volcano.sh/apis/pkg/apis/nodeinfo/v1alpha1"

	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/plugins/numaaware/policy"
	"volcano.sh/volcano/pkg/scheduler/plugins/numaaware/provider/gpumanager"
)

func podRequestsGPUs(pod *v1.Pod) bool {
	for _, containers := range [][]v1.Container{pod.Spec.Containers, pod.Spec.InitContainers} {
		for _, container := range containers {
			request, valid := gpumanager.RequestedGPUs(&container)
			if !valid || request > 0 {
				return true
			}
		}
	}
	return false
}

func validateStrictGPUTopology(topoInfo *api.NumatopoInfo, available api.ResNumaSets) error {
	if len(topoInfo.CPUDetail) == 0 || topoInfo.CPUDetail.NUMANodes().Size() > 16 {
		return fmt.Errorf("strict GPU locality requires valid CPU topology with at most 16 NUMA nodes")
	}
	for cpuID, detail := range topoInfo.CPUDetail {
		if _, err := bitmask.NewBitMask(detail.NUMANodeID); cpuID < 0 || err != nil {
			return fmt.Errorf("invalid topology for CPU %d", cpuID)
		}
	}
	for _, cpuID := range available[string(v1.ResourceCPU)].List() {
		if _, found := topoInfo.CPUDetail[cpuID]; !found {
			return fmt.Errorf("missing topology for CPU %d", cpuID)
		}
	}
	if !topoInfo.GPUDetail.NUMANodes().IsSubsetOf(topoInfo.CPUDetail.NUMANodes()) {
		return fmt.Errorf("GPU topology references unknown CPU NUMA nodes")
	}
	return nil
}

func (pp *numaPlugin) predicate(task *api.TaskInfo, node *api.NodeInfo, numaNodes []int) error {
	pp.Lock()
	delete(pp.assignRes[task.UID], node.Name)
	pp.Unlock()

	hasGPUs := podRequestsGPUs(task.Pod)
	strictGPU := hasGPUs && ((task.NumaInfo != nil && task.NumaInfo.Policy == "single-numa-node") ||
		(node.NumaSchedulerInfo != nil && node.NumaSchedulerInfo.Policies[nodeinfov1alpha1.TopologyManagerPolicy] == "single-numa-node"))
	guaranteed := v1qos.GetPodQOS(task.Pod) == v1.PodQOSGuaranteed
	if strictGPU {
		if !guaranteed {
			return api.NewFitError(task, node, "strict GPU locality requires a Guaranteed pod")
		}
		if len(task.Pod.Spec.InitContainers) != 0 {
			return api.NewFitError(task, node, "init containers are not supported for strict GPU locality")
		}
		for _, container := range task.Pod.Spec.Containers {
			gpuRequest, valid := gpumanager.RequestedGPUs(&container)
			if !valid {
				return api.NewFitError(task, node, fmt.Sprintf("container %s requires an integral GPU request", container.Name))
			}
			if gpuRequest > 0 {
				cpuRequest, valid := policy.IntegralQuantity(container.Resources.Requests[v1.ResourceCPU])
				if !valid || cpuRequest == 0 {
					return api.NewFitError(task, node, fmt.Sprintf("container %s requires a positive integral CPU request for strict GPU locality", container.Name))
				}
			}
		}
	}
	if !guaranteed && !hasGPUs {
		return nil
	}
	if fit, err := filterNodeByPolicy(task, node, pp.nodeResSets); !fit {
		if err != nil {
			return api.NewFitError(task, node, err.Error())
		}
		if strictGPU {
			return api.NewFitError(task, node, "strict GPU locality requires static CPU manager and NUMA topology")
		}
		return nil
	}

	resNumaSets := pp.nodeResSets[node.Name].Clone()
	if strictGPU {
		if err := validateStrictGPUTopology(node.NumaSchedulerInfo, resNumaSets); err != nil {
			return api.NewFitError(task, node, err.Error())
		}
	}
	providers := pp.hintProviders
	if !guaranteed {
		providers = []policy.HintProvider{gpumanager.NewProvider()}
	}
	taskPolicy := policy.GetPolicy(node, numaNodes)
	allResAssignMap := make(api.ResNumaSets)
	for _, container := range task.Pod.Spec.Containers {
		gpuRequest, valid := gpumanager.RequestedGPUs(&container)
		if !valid {
			return api.NewFitError(task, node, fmt.Sprintf("container %s requires an integral GPU request", container.Name))
		}
		providersHints := policy.AccumulateProvidersHints(&container, node.NumaSchedulerInfo, resNumaSets, providers)
		hint, admit := taskPolicy.Predicate(providersHints)
		if !admit {
			return api.NewFitErrWithStatus(task, node, &api.Status{
				Code:   api.UnschedulableAndUnresolvable,
				Reason: fmt.Sprintf("container %s cannot be assigned by numa", container.Name),
				Plugin: PluginName,
			})
		}

		available := resNumaSets
		if strictGPU && gpuRequest > 0 {
			if hint.NUMANodeAffinity == nil || hint.NUMANodeAffinity.Count() != 1 {
				return api.NewFitError(task, node, "strict GPU locality requires a single CPU/GPU NUMA node")
			}
			available = resNumaSets.Clone()
			available[string(v1.ResourceCPU)] = available[string(v1.ResourceCPU)].Intersection(
				node.NumaSchedulerInfo.CPUDetail.CPUsInNUMANodes(hint.NUMANodeAffinity.GetBits()...))
		}
		assignment := policy.Allocate(&container, &hint, node.NumaSchedulerInfo, available, providers)
		if gpuRequest > 0 {
			_, known := resNumaSets[string(gpumanager.NvidiaGPUResource)]
			if (strictGPU || known) && assignment[string(gpumanager.NvidiaGPUResource)].Size() != gpuRequest {
				return api.NewFitError(task, node, fmt.Sprintf("container %s has insufficient available GPUs", container.Name))
			}
			if strictGPU {
				cpuRequest, _ := policy.IntegralQuantity(container.Resources.Requests[v1.ResourceCPU])
				if assignment[string(v1.ResourceCPU)].Size() != cpuRequest {
					return api.NewFitError(task, node, fmt.Sprintf("container %s has insufficient aligned CPUs", container.Name))
				}
			}
		}
		for resourceName, assigned := range assignment {
			allResAssignMap[resourceName] = allResAssignMap[resourceName].Union(assigned)
			resNumaSets[resourceName] = resNumaSets[resourceName].Difference(assigned)
		}
	}

	pp.Lock()
	defer pp.Unlock()
	if _, found := pp.assignRes[task.UID]; !found {
		pp.assignRes[task.UID] = make(map[string]api.ResNumaSets)
	}
	pp.assignRes[task.UID][node.Name] = allResAssignMap
	return nil
}
