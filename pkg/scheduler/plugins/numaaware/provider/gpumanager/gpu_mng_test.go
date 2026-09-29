package gpumanager

import (
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/kubernetes/pkg/kubelet/cm/topologymanager/bitmask"
	"k8s.io/utils/cpuset"

	nodeinfov1alpha1 "volcano.sh/apis/pkg/apis/nodeinfo/v1alpha1"

	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/plugins/numaaware/policy"
)

func gpuContainer(request string) v1.Container {
	return v1.Container{Resources: v1.ResourceRequirements{
		Requests: v1.ResourceList{NvidiaGPUResource: resource.MustParse(request)},
	}}
}

func gpuTopology() *api.NumatopoInfo {
	return &api.NumatopoInfo{
		GPUDetail: api.GPUDetails{
			0: {NUMANodeID: 0}, 1: {NUMANodeID: 0},
			2: {NUMANodeID: 1}, 3: {NUMANodeID: 1},
		},
		Policies: map[nodeinfov1alpha1.PolicyName]string{nodeinfov1alpha1.TopologyManagerPolicy: "single-numa-node"},
	}
}

func TestGPUHints(test *testing.T) {
	for _, testcase := range []struct {
		name      string
		request   string
		available cpuset.CPUSet
		masks     []string
		preferred []bool
	}{
		{"native", "2", cpuset.New(0, 1, 2, 3), []string{"01", "10", "11"}, []bool{true, true, false}},
		{"depleted NUMA", "2", cpuset.New(0, 2, 3), []string{"10", "11"}, []bool{true, false}},
		{"fragmented", "2", cpuset.New(0, 2), []string{"11"}, []bool{false}},
		{"cross NUMA", "3", cpuset.New(0, 1, 2, 3), []string{"11"}, []bool{true}},
		{"insufficient", "5", cpuset.New(0, 1, 2, 3), nil, nil},
		{"depleted", "1", cpuset.New(), nil, nil},
		{"fractional", "1500m", cpuset.New(0, 1), nil, nil},
		{"negative", "-1", cpuset.New(0, 1), nil, nil},
		{"overflow", "9223372036854775808", cpuset.New(0, 1), nil, nil},
		{"huge", "1e30", cpuset.New(0, 1), nil, nil},
	} {
		test.Run(testcase.name, func(test *testing.T) {
			container := gpuContainer(testcase.request)
			got := NewProvider().GetTopologyHints(&container, gpuTopology(), api.ResNumaSets{string(NvidiaGPUResource): testcase.available})
			hints, found := got[string(NvidiaGPUResource)]
			if !found || hints == nil || len(hints) != len(testcase.masks) {
				test.Fatalf("hints = %#v, want %d non-nil hints", got, len(testcase.masks))
			}
			for index, hint := range hints {
				if hint.NUMANodeAffinity.String() != testcase.masks[index] || hint.Preferred != testcase.preferred[index] {
					test.Errorf("hint %d = %v, want %s preferred=%t", index, hint, testcase.masks[index], testcase.preferred[index])
				}
			}
		})
	}
}

func TestGPUHintsUnknownTopology(test *testing.T) {
	for _, name := range []string{"missing", "empty", "unknown resource", "unknown device", "negative NUMA", "large NUMA", "negative device", "too many NUMA"} {
		test.Run(name, func(test *testing.T) {
			container := gpuContainer("1")
			topoInfo := gpuTopology()
			available := api.ResNumaSets{string(NvidiaGPUResource): cpuset.New(0, 1, 2, 3)}
			switch name {
			case "missing":
				topoInfo = nil
			case "empty":
				topoInfo.GPUDetail = nil
			case "unknown resource":
				delete(available, string(NvidiaGPUResource))
			case "unknown device":
				delete(topoInfo.GPUDetail, 3)
			case "negative NUMA":
				topoInfo.GPUDetail[0] = api.GPUInfo{NUMANodeID: -1}
			case "large NUMA":
				topoInfo.GPUDetail[0] = api.GPUInfo{NUMANodeID: 64}
			case "negative device":
				topoInfo.GPUDetail[-1] = api.GPUInfo{NUMANodeID: 0}
			case "too many NUMA":
				for index := 0; index <= maxNUMANodes; index++ {
					topoInfo.GPUDetail[index] = api.GPUInfo{NUMANodeID: index}
				}
			}
			hints := NewProvider().GetTopologyHints(&container, topoInfo, available)[string(NvidiaGPUResource)]
			if hints == nil || len(hints) != 0 {
				test.Fatalf("hints = %#v, want non-nil empty hints", hints)
			}
			if _, admit := policy.NewPolicySingleNumaNode([]int{0, 1}).Predicate([]map[string][]policy.TopologyHint{{string(NvidiaGPUResource): hints}}); admit {
				test.Fatal("strict policy admitted unknown GPU topology")
			}
			if _, admit := policy.NewPolicyBestEffort([]int{0, 1}).Predicate([]map[string][]policy.TopologyHint{{string(NvidiaGPUResource): hints}}); !admit {
				test.Fatal("best-effort policy rejected topology fallback")
			}
			if topoInfo != nil {
				mask, _ := bitmask.NewBitMask(0)
				hint := &policy.TopologyHint{NUMANodeAffinity: mask, Preferred: true}
				if assignment := NewProvider().Allocate(&container, hint, topoInfo, available); assignment != nil {
					test.Fatalf("strict allocation accepted unknown topology: %v", assignment)
				}
			}
		})
	}
}

func TestOnlyNativeGPURequests(test *testing.T) {
	for _, container := range []v1.Container{
		{}, gpuContainer("0"),
		{Resources: v1.ResourceRequirements{Requests: v1.ResourceList{"volcano.sh/vgpu-number": resource.MustParse("1")}}},
		{Resources: v1.ResourceRequirements{Requests: v1.ResourceList{"nvidia.com/mig-1g.5gb": resource.MustParse("1")}}},
	} {
		if hints := NewProvider().GetTopologyHints(&container, nil, nil); hints != nil {
			test.Errorf("non-native/zero request produced hints: %v", hints)
		}
		if assignment := NewProvider().Allocate(&container, nil, nil, nil); assignment != nil {
			test.Errorf("non-native/zero request produced assignment: %v", assignment)
		}
	}
	container := v1.Container{Resources: v1.ResourceRequirements{Limits: v1.ResourceList{NvidiaGPUResource: resource.MustParse("1")}}}
	if request, valid := RequestedGPUs(&container); !valid || request != 1 {
		test.Fatalf("limits-only GPU request = %d, %t", request, valid)
	}
}

func TestGPUAllocate(test *testing.T) {
	mask, _ := bitmask.NewBitMask(0)
	for _, testcase := range []struct {
		name      string
		request   string
		available cpuset.CPUSet
		strict    bool
		hint      *policy.TopologyHint
		expected  cpuset.CPUSet
	}{
		{"aligned", "2", cpuset.New(0, 1, 2, 3), true, &policy.TopologyHint{NUMANodeAffinity: mask, Preferred: true}, cpuset.New(0, 1)},
		{"strict no spillover", "2", cpuset.New(0, 2, 3), true, &policy.TopologyHint{NUMANodeAffinity: mask, Preferred: true}, cpuset.New()},
		{"strict no hint", "1", cpuset.New(0, 1), true, nil, cpuset.New()},
		{"best effort spillover", "2", cpuset.New(0, 2, 3), false, &policy.TopologyHint{NUMANodeAffinity: mask}, cpuset.New(0, 2)},
		{"best effort no hint", "2", cpuset.New(0, 1), false, nil, cpuset.New(0, 1)},
		{"no partial assignment", "3", cpuset.New(0, 1), false, nil, cpuset.New()},
		{"depleted", "1", cpuset.New(), false, nil, cpuset.New()},
		{"overflow", "9223372036854775808", cpuset.New(0, 1), false, nil, cpuset.New()},
	} {
		test.Run(testcase.name, func(test *testing.T) {
			container := gpuContainer(testcase.request)
			topoInfo := gpuTopology()
			if !testcase.strict {
				topoInfo.Policies[nodeinfov1alpha1.TopologyManagerPolicy] = "best-effort"
			}
			available := api.ResNumaSets{string(NvidiaGPUResource): testcase.available.Clone()}
			assignment := NewProvider().Allocate(&container, testcase.hint, topoInfo, available)
			if !testcase.expected.IsEmpty() && !assignment[string(NvidiaGPUResource)].Equals(testcase.expected) {
				test.Fatalf("assignment = %v, want %v", assignment, testcase.expected)
			}
			if testcase.expected.IsEmpty() && assignment != nil {
				test.Fatalf("failed allocation returned a resource entry: %v", assignment)
			}
			if !available[string(NvidiaGPUResource)].Equals(testcase.available) {
				test.Fatal("allocation modified available devices")
			}
		})
	}
}
