package api

import (
	"reflect"
	"testing"

	"k8s.io/kubernetes/pkg/kubelet/cm/cpumanager/topology"
	"k8s.io/utils/cpuset"

	nodeinfov1alpha1 "volcano.sh/apis/pkg/apis/nodeinfo/v1alpha1"
)

func TestGPUDetails(test *testing.T) {
	details := GPUDetails{
		0: {NUMANodeID: 0},
		1: {NUMANodeID: 1},
		2: {NUMANodeID: 1},
		3: {NUMANodeID: -1},
		4: {NUMANodeID: 64},
	}
	if actual := details.NUMANodes(); !actual.Equals(cpuset.New(0, 1)) {
		test.Fatalf("NUMANodes() = %v", actual)
	}
	if actual := details.GPUsInNUMANodes(1); !actual.Equals(cpuset.New(1, 2)) {
		test.Fatalf("GPUsInNUMANodes(1) = %v", actual)
	}
	if actual := details.GPUsInNUMANodes(0, 1); !actual.Equals(cpuset.New(0, 1, 2)) {
		test.Fatalf("GPUsInNUMANodes(0, 1) = %v", actual)
	}
	if actual := details.GPUsInNUMANodes(-1, 64); actual.Size() != 0 {
		test.Fatalf("invalid NUMA nodes returned GPUs: %v", actual)
	}
}

func TestNumaGPUDeepCopy(test *testing.T) {
	original := &NumatopoInfo{GPUDetail: GPUDetails{0: {NUMANodeID: 1}}}
	clone := original.DeepCopy()
	clone.GPUDetail[0] = GPUInfo{NUMANodeID: 0}
	if original.GPUDetail[0].NUMANodeID != 1 {
		test.Fatal("GPU topology map is shared by scheduler snapshots")
	}
	apiOriginal := &nodeinfov1alpha1.Numatopology{
		Spec: nodeinfov1alpha1.NumatopoSpec{
			GPUDetail: map[string]nodeinfov1alpha1.GPUInfo{"0": {NUMANodeID: 1, BusID: "0000:01:00.0"}},
		},
	}
	apiClone := apiOriginal.DeepCopy()
	apiClone.Spec.GPUDetail["0"] = nodeinfov1alpha1.GPUInfo{NUMANodeID: 0}
	if apiOriginal.Spec.GPUDetail["0"].NUMANodeID != 1 {
		test.Fatal("GPU topology map is shared by API copies")
	}
}

func TestNumaCompareAllResources(test *testing.T) {
	previous := &NumatopoInfo{NumaResMap: map[string]*ResourceInfo{
		"cpu":            {Allocatable: cpuset.New(0, 1)},
		"nvidia.com/gpu": {Allocatable: cpuset.New(0, 1)},
	}}
	testCases := []struct {
		name      string
		resources map[string]*ResourceInfo
		more      bool
	}{
		{
			name: "unchanged CPU cannot hide fewer GPUs",
			resources: map[string]*ResourceInfo{
				"cpu":            {Allocatable: cpuset.New(0, 1)},
				"nvidia.com/gpu": {Allocatable: cpuset.New(1)},
			},
		},
		{
			name: "more CPUs cannot hide fewer GPUs",
			resources: map[string]*ResourceInfo{
				"cpu":            {Allocatable: cpuset.New(0, 1, 2)},
				"nvidia.com/gpu": {Allocatable: cpuset.New(1)},
			},
		},
		{
			name: "same count different devices",
			resources: map[string]*ResourceInfo{
				"cpu":            {Allocatable: cpuset.New(0, 1)},
				"nvidia.com/gpu": {Allocatable: cpuset.New(1, 2)},
			},
		},
		{
			name:      "GPU resource removed",
			resources: map[string]*ResourceInfo{"cpu": {Allocatable: cpuset.New(0, 1)}},
		},
		{
			name:      "GPU resource unavailable",
			resources: map[string]*ResourceInfo{"cpu": {Allocatable: cpuset.New(0, 1)}, "nvidia.com/gpu": nil},
		},
		{
			name: "all resources increased",
			resources: map[string]*ResourceInfo{
				"cpu":            {Allocatable: cpuset.New(0, 1, 2)},
				"nvidia.com/gpu": {Allocatable: cpuset.New(0, 1, 2)},
			},
			more: true,
		},
	}
	for _, testCase := range testCases {
		test.Run(testCase.name, func(test *testing.T) {
			if actual := previous.Compare(&NumatopoInfo{NumaResMap: testCase.resources}); actual != testCase.more {
				test.Fatalf("Compare() = %v, want %v", actual, testCase.more)
			}
		})
	}
}

func TestRefreshNumaGPUReconcilesReportedResources(test *testing.T) {
	node := &NodeInfo{
		NumaChgFlag: NumaInfoLessFlag,
		NumaSchedulerInfo: &NumatopoInfo{
			NumaResMap: map[string]*ResourceInfo{
				"cpu":            {Allocatable: cpuset.New(1, 2)},
				"nvidia.com/gpu": {Allocatable: cpuset.New(1, 2)},
				"removed":        {Allocatable: cpuset.New(0)},
			},
		},
		NumaInfo: &NumatopoInfo{
			NumaResMap: map[string]*ResourceInfo{
				"cpu":            {Allocatable: cpuset.New(0, 1, 2, 3), Capacity: 4},
				"nvidia.com/gpu": {Allocatable: cpuset.New(0, 2), Capacity: 3},
			},
			GPUDetail: GPUDetails{0: {NUMANodeID: 0}, 2: {NUMANodeID: 1}},
			CPUDetail: topology.CPUDetails{0: {NUMANodeID: 0}, 1: {NUMANodeID: 1}},
		},
	}
	node.RefreshNumaSchedulerInfoByCrd()
	if actual := node.NumaSchedulerInfo.NumaResMap["cpu"].Allocatable; !actual.Equals(cpuset.New(0, 1, 2, 3)) {
		test.Fatalf("newly available CPUs not reconciled: %v", actual)
	}
	if actual := node.NumaSchedulerInfo.NumaResMap["nvidia.com/gpu"].Allocatable; !actual.Equals(cpuset.New(0, 2)) {
		test.Fatalf("actual kubelet GPU selection not reconciled: %v", actual)
	}
	if _, exists := node.NumaSchedulerInfo.NumaResMap["removed"]; exists {
		test.Fatal("removed resource retained")
	}
	if !reflect.DeepEqual(node.NumaSchedulerInfo.GPUDetail, node.NumaInfo.GPUDetail) {
		test.Fatal("GPU topology was not refreshed")
	}
	if node.NumaChgFlag != NumaInfoResetFlag {
		test.Fatal("NUMA change flag not reset")
	}
}

func TestRefreshNumaGPUReplacementRecoversFreeDevices(test *testing.T) {
	node := &NodeInfo{
		NumaInfo: &NumatopoInfo{NumaResMap: map[string]*ResourceInfo{
			"cpu":            {Allocatable: cpuset.New(0, 1)},
			"nvidia.com/gpu": {Allocatable: cpuset.New(0)},
		}},
	}
	node.RefreshNumaSchedulerInfoByCrd()
	node.NumaInfo.NumaResMap["nvidia.com/gpu"].Allocatable = cpuset.New(1)
	node.NumaChgFlag = NumaInfoLessFlag
	node.RefreshNumaSchedulerInfoByCrd()
	if actual := node.NumaSchedulerInfo.NumaResMap["nvidia.com/gpu"].Allocatable; !actual.Equals(cpuset.New(1)) {
		test.Fatalf("newly freed GPU disappeared on equal-sized report: %v", actual)
	}

	node.NumaSchedulerInfo.Allocate(ResNumaSets{"cpu": cpuset.New(0), "nvidia.com/gpu": cpuset.New(1)})
	node.RefreshNumaSchedulerInfoByCrd()
	if actual := node.NumaSchedulerInfo.NumaResMap["nvidia.com/gpu"].Allocatable; actual.Size() != 0 {
		test.Fatalf("session without a new report restored an assumed GPU: %v", actual)
	}
	node.NumaInfo.NumaResMap["cpu"].Allocatable = cpuset.New(0, 1, 2)
	node.NumaInfo.NumaResMap["nvidia.com/gpu"].Allocatable = cpuset.New(0, 1)
	node.NumaChgFlag = NumaInfoMoreFlag
	node.RefreshNumaSchedulerInfoByCrd()
	if actual := node.NumaSchedulerInfo.NumaResMap["nvidia.com/gpu"].Allocatable; !actual.Equals(cpuset.New(0, 1)) {
		test.Fatalf("released GPUs retained phantom reservations: %v", actual)
	}
	if actual := node.NumaSchedulerInfo.NumaResMap["cpu"].Allocatable; !actual.Equals(cpuset.New(0, 1, 2)) {
		test.Fatalf("released CPUs retained phantom reservations: %v", actual)
	}
}
