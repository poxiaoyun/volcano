package numaaware

import (
	"strings"
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/kubernetes/pkg/kubelet/cm/cpumanager/topology"
	"k8s.io/utils/cpuset"

	nodeinfov1alpha1 "volcano.sh/apis/pkg/apis/nodeinfo/v1alpha1"

	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/plugins/numaaware/provider/gpumanager"
	"volcano.sh/volcano/pkg/scheduler/plugins/util"
)

func gpuTask() *api.TaskInfo {
	requests := v1.ResourceList{
		v1.ResourceCPU:               resource.MustParse("1"),
		v1.ResourceMemory:            resource.MustParse("1Gi"),
		gpumanager.NvidiaGPUResource: resource.MustParse("1"),
	}
	return &api.TaskInfo{
		UID: "task", Name: "task",
		NumaInfo: &api.TopologyInfo{Policy: "single-numa-node"},
		Pod: &v1.Pod{Spec: v1.PodSpec{Containers: []v1.Container{{
			Name: "worker", Resources: v1.ResourceRequirements{Requests: requests, Limits: requests.DeepCopy()},
		}}}},
	}
}

func gpuNode() *api.NodeInfo {
	return &api.NodeInfo{
		Name: "node",
		NumaSchedulerInfo: &api.NumatopoInfo{
			CPUDetail: topology.CPUDetails{
				0: {NUMANodeID: 0, CoreID: 0, SocketID: 0},
				1: {NUMANodeID: 0, CoreID: 1, SocketID: 0},
				2: {NUMANodeID: 1, CoreID: 2, SocketID: 1},
				3: {NUMANodeID: 1, CoreID: 3, SocketID: 1},
			},
			GPUDetail: api.GPUDetails{
				0: {NUMANodeID: 0}, 1: {NUMANodeID: 0},
				2: {NUMANodeID: 1}, 3: {NUMANodeID: 1},
			},
			Policies: map[nodeinfov1alpha1.PolicyName]string{
				nodeinfov1alpha1.CPUManagerPolicy:      "static",
				nodeinfov1alpha1.TopologyManagerPolicy: "single-numa-node",
			},
			NumaResMap: map[string]*api.ResourceInfo{
				string(v1.ResourceCPU):               {Allocatable: cpuset.New(0, 1, 2, 3), Capacity: 4},
				string(gpumanager.NvidiaGPUResource): {Allocatable: cpuset.New(0, 1, 2, 3), Capacity: 4},
			},
		},
	}
}

func TestStrictGPUPredicate(test *testing.T) {
	for _, testcase := range []struct {
		name   string
		change func(*api.TaskInfo, *api.NodeInfo)
		error  string
	}{
		{"same NUMA", func(task *api.TaskInfo, node *api.NodeInfo) {}, ""},
		{"disjoint CPU GPU", func(task *api.TaskInfo, node *api.NodeInfo) {
			node.NumaSchedulerInfo.NumaResMap["cpu"].Allocatable = cpuset.New(0, 1)
			node.NumaSchedulerInfo.NumaResMap[string(gpumanager.NvidiaGPUResource)].Allocatable = cpuset.New(2, 3)
		}, "cannot be assigned"},
		{"missing node topology", func(task *api.TaskInfo, node *api.NodeInfo) {
			node.NumaSchedulerInfo = nil
		}, "numa info is empty"},
		{"missing GPU topology", func(task *api.TaskInfo, node *api.NodeInfo) {
			node.NumaSchedulerInfo.GPUDetail = nil
		}, "cannot be assigned"},
		{"missing CPU topology", func(task *api.TaskInfo, node *api.NodeInfo) {
			node.NumaSchedulerInfo.CPUDetail = nil
		}, "valid CPU topology"},
		{"unknown CPU", func(task *api.TaskInfo, node *api.NodeInfo) {
			delete(node.NumaSchedulerInfo.CPUDetail, 0)
		}, "missing topology for CPU"},
		{"invalid CPU NUMA", func(task *api.TaskInfo, node *api.NodeInfo) {
			node.NumaSchedulerInfo.CPUDetail[0] = topology.CPUInfo{NUMANodeID: -1}
		}, "invalid topology for CPU"},
		{"invalid GPU NUMA", func(task *api.TaskInfo, node *api.NodeInfo) {
			node.NumaSchedulerInfo.GPUDetail[0] = api.GPUInfo{NUMANodeID: -1}
		}, "cannot be assigned"},
		{"unknown GPU NUMA", func(task *api.TaskInfo, node *api.NodeInfo) {
			node.NumaSchedulerInfo.GPUDetail[0] = api.GPUInfo{NUMANodeID: 2}
		}, "unknown CPU NUMA"},
		{"unknown GPU resource", func(task *api.TaskInfo, node *api.NodeInfo) {
			delete(node.NumaSchedulerInfo.NumaResMap, string(gpumanager.NvidiaGPUResource))
		}, "cannot be assigned"},
		{"depleted GPUs", func(task *api.TaskInfo, node *api.NodeInfo) {
			node.NumaSchedulerInfo.NumaResMap[string(gpumanager.NvidiaGPUResource)].Allocatable = cpuset.New()
		}, "cannot be assigned"},
		{"burst GPU", func(task *api.TaskInfo, node *api.NodeInfo) {
			task.Pod.Spec.Containers[0].Resources.Limits = nil
		}, "Guaranteed"},
		{"fractional CPU", func(task *api.TaskInfo, node *api.NodeInfo) {
			task.Pod.Spec.Containers[0].Resources.Requests[v1.ResourceCPU] = resource.MustParse("500m")
			task.Pod.Spec.Containers[0].Resources.Limits[v1.ResourceCPU] = resource.MustParse("500m")
		}, "positive integral CPU"},
		{"missing CPU", func(task *api.TaskInfo, node *api.NodeInfo) {
			delete(task.Pod.Spec.Containers[0].Resources.Requests, v1.ResourceCPU)
			delete(task.Pod.Spec.Containers[0].Resources.Limits, v1.ResourceCPU)
		}, "Guaranteed"},
		{"zero CPU", func(task *api.TaskInfo, node *api.NodeInfo) {
			task.Pod.Spec.Containers[0].Resources.Requests[v1.ResourceCPU] = resource.MustParse("0")
			task.Pod.Spec.Containers[0].Resources.Limits[v1.ResourceCPU] = resource.MustParse("0")
		}, "Guaranteed"},
		{"second GPU container fractional CPU", func(task *api.TaskInfo, node *api.NodeInfo) {
			task.Pod.Spec.Containers = append(task.Pod.Spec.Containers, *task.Pod.Spec.Containers[0].DeepCopy())
			task.Pod.Spec.Containers[1].Resources.Requests[v1.ResourceCPU] = resource.MustParse("500m")
			task.Pod.Spec.Containers[1].Resources.Limits[v1.ResourceCPU] = resource.MustParse("500m")
		}, "positive integral CPU"},
		{"CPU overflow", func(task *api.TaskInfo, node *api.NodeInfo) {
			task.Pod.Spec.Containers[0].Resources.Requests[v1.ResourceCPU] = resource.MustParse("1e30")
			task.Pod.Spec.Containers[0].Resources.Limits[v1.ResourceCPU] = resource.MustParse("1e30")
		}, "positive integral CPU"},
		{"GPU overflow", func(task *api.TaskInfo, node *api.NodeInfo) {
			task.Pod.Spec.Containers[0].Resources.Requests[gpumanager.NvidiaGPUResource] = resource.MustParse("1e30")
		}, "integral GPU"},
		{"node strict without task policy", func(task *api.TaskInfo, node *api.NodeInfo) {
			task.NumaInfo = nil
			task.Pod.Spec.Containers[0].Resources.Limits = nil
		}, "Guaranteed"},
		{"implicit strict without static CPU manager", func(task *api.TaskInfo, node *api.NodeInfo) {
			task.NumaInfo = nil
			delete(node.NumaSchedulerInfo.Policies, nodeinfov1alpha1.CPUManagerPolicy)
		}, "static CPU manager"},
		{"init container", func(task *api.TaskInfo, node *api.NodeInfo) {
			task.Pod.Spec.InitContainers = []v1.Container{*task.Pod.Spec.Containers[0].DeepCopy()}
		}, "init containers"},
		{"GPU only in init", func(task *api.TaskInfo, node *api.NodeInfo) {
			task.Pod.Spec.InitContainers = []v1.Container{*task.Pod.Spec.Containers[0].DeepCopy()}
			delete(task.Pod.Spec.Containers[0].Resources.Requests, gpumanager.NvidiaGPUResource)
			delete(task.Pod.Spec.Containers[0].Resources.Limits, gpumanager.NvidiaGPUResource)
		}, "init containers"},
	} {
		test.Run(testcase.name, func(test *testing.T) {
			task, node := gpuTask(), gpuNode()
			testcase.change(task, node)
			plugin := New(nil).(*numaPlugin)
			plugin.nodeResSets = api.GenerateNodeResNumaSets(map[string]*api.NodeInfo{node.Name: node})
			before := plugin.nodeResSets[node.Name].Clone()
			err := plugin.predicate(task, node, []int{0, 1})
			if testcase.error != "" {
				if err == nil || !strings.Contains(err.Error(), testcase.error) {
					test.Fatalf("error = %v, want %q", err, testcase.error)
				}
				if _, found := plugin.assignRes[task.UID][node.Name]; found {
					test.Fatal("failed predicate cached a partial assignment")
				}
			} else {
				if err != nil {
					test.Fatal(err)
				}
				assignment := plugin.assignRes[task.UID][node.Name]
				if assignment["cpu"].Size() != 1 || assignment[string(gpumanager.NvidiaGPUResource)].Size() != 1 {
					test.Fatalf("incomplete assignment: %v", assignment)
				}
				if count := getNodeNumaNumForTask([]*api.NodeInfo{node}, plugin.assignRes[task.UID])[0].Score; count != 1 {
					test.Fatalf("assignment spans %d NUMA nodes", count)
				}
			}
			for resourceName, available := range before {
				if !plugin.nodeResSets[node.Name][resourceName].Equals(available) {
					test.Fatal("predicate mutated session availability")
				}
			}
		})
	}
}

func TestMultipleGPUContainers(test *testing.T) {
	for _, enough := range []bool{true, false} {
		task, node := gpuTask(), gpuNode()
		task.Pod.Spec.Containers = append(task.Pod.Spec.Containers, *task.Pod.Spec.Containers[0].DeepCopy())
		task.Pod.Spec.Containers[1].Name = "second"
		node.NumaSchedulerInfo.NumaResMap["cpu"].Allocatable = cpuset.New(0, 2)
		node.NumaSchedulerInfo.NumaResMap[string(gpumanager.NvidiaGPUResource)].Allocatable = cpuset.New(0, 2)
		if !enough {
			node.NumaSchedulerInfo.NumaResMap[string(gpumanager.NvidiaGPUResource)].Allocatable = cpuset.New(0)
		}
		plugin := New(nil).(*numaPlugin)
		plugin.nodeResSets = api.GenerateNodeResNumaSets(map[string]*api.NodeInfo{node.Name: node})
		err := plugin.predicate(task, node, []int{0, 1})
		if !enough {
			if err == nil || len(plugin.assignRes[task.UID]) != 0 {
				test.Fatalf("overcommitted containers: error=%v assignment=%v", err, plugin.assignRes)
			}
			continue
		}
		if err != nil {
			test.Fatal(err)
		}
		assignment := plugin.assignRes[task.UID][node.Name]
		if !assignment["cpu"].Equals(cpuset.New(0, 2)) || !assignment[string(gpumanager.NvidiaGPUResource)].Equals(cpuset.New(0, 2)) {
			test.Fatalf("containers reused CPU/GPU resources: %v", assignment)
		}
		plugin.nodeResSets[node.Name].Allocate(assignment)
		if err := plugin.predicate(task, node, []int{0, 1}); err == nil {
			test.Fatal("depleted resources admitted a repeated allocation")
		}
		if _, found := plugin.assignRes[task.UID][node.Name]; found {
			test.Fatal("failed predicate retained a stale assignment")
		}
	}
}

func TestBestEffortAndNonGPUBehavior(test *testing.T) {
	for _, name := range []string{"disjoint", "missing topology", "unknown resource", "insufficient GPUs", "burstable GPU", "CPU only", "burstable CPU"} {
		test.Run(name, func(test *testing.T) {
			task, node := gpuTask(), gpuNode()
			task.NumaInfo.Policy = "best-effort"
			node.NumaSchedulerInfo.Policies[nodeinfov1alpha1.TopologyManagerPolicy] = "best-effort"
			node.NumaSchedulerInfo.NumaResMap["cpu"].Allocatable = cpuset.New(0, 1)
			node.NumaSchedulerInfo.NumaResMap[string(gpumanager.NvidiaGPUResource)].Allocatable = cpuset.New(2, 3)
			switch name {
			case "missing topology":
				node.NumaSchedulerInfo.GPUDetail = nil
			case "unknown resource":
				delete(node.NumaSchedulerInfo.NumaResMap, string(gpumanager.NvidiaGPUResource))
			case "insufficient GPUs":
				node.NumaSchedulerInfo.NumaResMap[string(gpumanager.NvidiaGPUResource)].Allocatable = cpuset.New()
			case "burstable GPU":
				task.Pod.Spec.Containers[0].Resources.Limits = nil
			case "CPU only", "burstable CPU":
				delete(task.Pod.Spec.Containers[0].Resources.Requests, gpumanager.NvidiaGPUResource)
				delete(task.Pod.Spec.Containers[0].Resources.Limits, gpumanager.NvidiaGPUResource)
				if name == "burstable CPU" {
					task.Pod.Spec.Containers[0].Resources.Limits = nil
				}
			}
			plugin := New(nil).(*numaPlugin)
			plugin.nodeResSets = api.GenerateNodeResNumaSets(map[string]*api.NodeInfo{node.Name: node})
			err := plugin.predicate(task, node, []int{0, 1})
			if name == "insufficient GPUs" {
				if err == nil {
					test.Fatal("best-effort admitted exhausted GPU resources")
				}
				return
			}
			if err != nil {
				test.Fatal(err)
			}
			assignment := plugin.assignRes[task.UID][node.Name]
			if name == "burstable CPU" && len(assignment) != 0 {
				test.Fatalf("non-Guaranteed CPU behavior changed: %v", assignment)
			}
			if name == "burstable GPU" && (assignment["cpu"].Size() != 0 || assignment[string(gpumanager.NvidiaGPUResource)].Size() != 1) {
				test.Fatalf("best-effort GPU pod received incorrect resources: %v", assignment)
			}
			if name == "disjoint" && getNodeNumaNumForTask([]*api.NodeInfo{node}, plugin.assignRes[task.UID])[0].Score != 2 {
				test.Fatal("best-effort fallback did not account for both NUMA nodes")
			}
		})
	}
}

func TestNUMAScoringUnion(test *testing.T) {
	node := gpuNode()
	for _, testcase := range []struct {
		name     string
		cpus     cpuset.CPUSet
		gpus     cpuset.CPUSet
		expected int64
	}{
		{"same NUMA", cpuset.New(0, 1), cpuset.New(0, 1), 1},
		{"disjoint", cpuset.New(0, 1), cpuset.New(2, 3), 2},
		{"overlapping", cpuset.New(0, 2), cpuset.New(2), 2},
		{"CPU only", cpuset.New(0, 2), cpuset.New(), 2},
		{"GPU only", cpuset.New(), cpuset.New(0, 2), 2},
		{"unknown GPU not NUMA zero", cpuset.New(2), cpuset.New(99), 1},
	} {
		test.Run(testcase.name, func(test *testing.T) {
			assignment := map[string]api.ResNumaSets{node.Name: {
				"cpu": testcase.cpus, string(gpumanager.NvidiaGPUResource): testcase.gpus,
			}}
			if got := getNodeNumaNumForTask([]*api.NodeInfo{node}, assignment)[0].Score; got != testcase.expected {
				test.Fatalf("NUMA count = %d, want %d", got, testcase.expected)
			}
		})
	}
	other := gpuNode()
	other.Name = "other"
	assignment := map[string]api.ResNumaSets{
		node.Name:  {"cpu": cpuset.New(0), string(gpumanager.NvidiaGPUResource): cpuset.New(0)},
		other.Name: {"cpu": cpuset.New(0), string(gpumanager.NvidiaGPUResource): cpuset.New(2)},
	}
	scores := getNodeNumaNumForTask([]*api.NodeInfo{node, other}, assignment)
	util.NormalizeScore(api.DefaultMaxNodeScore, true, scores)
	if scores[0].Score <= scores[1].Score {
		test.Fatalf("aligned assignment did not score higher: %v", scores)
	}
	other.NumaSchedulerInfo = nil
	if got := getNodeNumaNumForTask([]*api.NodeInfo{node, other}, assignment)[1].Score; got != 0 {
		test.Fatalf("missing topology score = %d, want 0", got)
	}
	providers := New(nil).(*numaPlugin).hintProviders
	if len(providers) != 2 || providers[0].Name() != "cpuMng" || providers[1].Name() != "gpuMng" {
		test.Fatalf("unexpected provider registration: %v", providers)
	}
}
