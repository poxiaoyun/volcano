package cache

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/cpuset"

	nodeinfov1alpha1 "volcano.sh/apis/pkg/apis/nodeinfo/v1alpha1"
	"volcano.sh/volcano/pkg/scheduler/api"
)

func TestGetNumaInfoGPU(test *testing.T) {
	source := &nodeinfov1alpha1.Numatopology{
		Spec: nodeinfov1alpha1.NumatopoSpec{
			CPUDetail: map[string]nodeinfov1alpha1.CPUInfo{"0": {NUMANodeID: 0}, "1": {NUMANodeID: 1}},
			GPUDetail: map[string]nodeinfov1alpha1.GPUInfo{
				"0":            {NUMANodeID: 0},
				"1":            {NUMANodeID: 1},
				"2":            {NUMANodeID: -1},
				"3":            {NUMANodeID: 64},
				"4":            {NUMANodeID: 2},
				"-1":           {NUMANodeID: 0},
				"not-an-index": {NUMANodeID: 0},
			},
			NumaResMap: map[string]nodeinfov1alpha1.ResourceInfo{
				"nvidia.com/gpu": {Allocatable: "0-5", Capacity: 6},
			},
		},
	}
	actual := getNumaInfo(source)
	if len(actual.GPUDetail) != 2 || actual.GPUDetail[1].NUMANodeID != 1 {
		test.Fatalf("unexpected GPU topology: %v", actual.GPUDetail)
	}
	if !actual.NumaResMap["nvidia.com/gpu"].Allocatable.Equals(cpuset.New(0, 1)) {
		test.Fatalf("invalid topology remained allocatable: %v", actual.NumaResMap)
	}
	delete(source.Spec.GPUDetail, "1")
	if len(actual.GPUDetail) != 2 {
		test.Fatal("cache topology aliases informer object")
	}
}

func TestGetNumaInfoMissingGPUTopology(test *testing.T) {
	source := &nodeinfov1alpha1.Numatopology{
		Spec: nodeinfov1alpha1.NumatopoSpec{
			NumaResMap: map[string]nodeinfov1alpha1.ResourceInfo{
				"nvidia.com/gpu": {Allocatable: "0-1", Capacity: 2},
			},
		},
	}
	actual := getNumaInfo(source)
	if actual.NumaResMap["nvidia.com/gpu"].Allocatable.Size() != 0 {
		test.Fatal("GPU without topology remained allocatable")
	}
}

func TestNumaInformerResyncPreservesAssignments(test *testing.T) {
	node := &api.NodeInfo{NumaChgFlag: api.NumaInfoResetFlag}
	cache := &SchedulerCache{Nodes: map[string]*api.NodeInfo{"node": node}}
	report := &nodeinfov1alpha1.Numatopology{ObjectMeta: metav1.ObjectMeta{Name: "node", ResourceVersion: "1"}}
	cache.UpdateNumaInfoV1alpha1(report, report.DeepCopy())
	if node.NumaChgFlag != api.NumaInfoResetFlag || node.NumaInfo != nil {
		test.Fatal("informer resync was treated as a new allocation report")
	}
	updated := report.DeepCopy()
	updated.ResourceVersion = "2"
	cache.UpdateNumaInfoV1alpha1(report, updated)
	if node.NumaChgFlag == api.NumaInfoResetFlag || node.NumaInfo == nil {
		test.Fatal("new allocation report was ignored")
	}
}
