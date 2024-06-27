package loadaware

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	v12 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

func TestAddPod(t *testing.T) {
	t.Parallel()

	c := &Cache{
		NodePodInfo: map[string]*NodeCache{},
	}

	c.addPod("testNode", &v1.Pod{
		ObjectMeta: v12.ObjectMeta{
			Name: "testPod",
			UID:  "testPod",
		},
	}, time.Now())
	assert.Equal(t, 1, len(c.NodePodInfo["testNode"].PodInfoMap))

	c.addPod("testNode", nil, time.Now())
	assert.Equal(t, 1, len(c.NodePodInfo["testNode"].PodInfoMap))

	c.addPod("testNode", &v1.Pod{
		ObjectMeta: v12.ObjectMeta{
			Name: "testPod",
			UID:  "testPod",
		},
	}, time.Now())
	assert.Equal(t, 1, len(c.NodePodInfo["testNode"].PodInfoMap))

	c.addPod("testNode", &v1.Pod{
		ObjectMeta: v12.ObjectMeta{
			Name: "testPod2",
			UID:  "testPod2",
		},
	}, time.Now())
	assert.Equal(t, 2, len(c.NodePodInfo["testNode"].PodInfoMap))

	c.addPod("testNode2", &v1.Pod{
		ObjectMeta: v12.ObjectMeta{
			Name: "testPod2",
			UID:  "testPod2",
		},
	}, time.Now())
	assert.Equal(t, 2, len(c.NodePodInfo))

	c.removePod("testNode2", &v1.Pod{
		ObjectMeta: v12.ObjectMeta{
			Name: "testPod2",
			UID:  "testPod2",
		},
	})
	assert.Equal(t, 1, len(c.NodePodInfo))

	c.removePod("testNode", &v1.Pod{
		ObjectMeta: v12.ObjectMeta{
			Name: "testPod2",
			UID:  "testPod2",
		}})
	assert.Equal(t, 1, len(c.NodePodInfo["testNode"].PodInfoMap))
}

type testSharedLister struct {
	nodes       []*v1.Node
	nodeInfos   []*framework.NodeInfo
	nodeInfoMap map[string]*framework.NodeInfo
}

func (f *testSharedLister) NodeInfos() framework.NodeInfoLister {
	return f
}

func (f *testSharedLister) List() ([]*framework.NodeInfo, error) {
	return f.nodeInfos, nil
}

func (f *testSharedLister) HavePodsWithAffinityList() ([]*framework.NodeInfo, error) {
	return nil, nil
}

func (f *testSharedLister) HavePodsWithRequiredAntiAffinityList() ([]*framework.NodeInfo, error) {
	return nil, nil
}

func (f *testSharedLister) Get(nodeName string) (*framework.NodeInfo, error) {
	return f.nodeInfoMap[nodeName], nil
}

func newTestSharedLister(pods []*v1.Pod, nodes []*v1.Node) *testSharedLister {
	nodeInfoMap := make(map[string]*framework.NodeInfo)
	nodeInfos := make([]*framework.NodeInfo, 0)
	for _, pod := range pods {
		nodeName := pod.Spec.NodeName
		if _, ok := nodeInfoMap[nodeName]; !ok {
			nodeInfoMap[nodeName] = framework.NewNodeInfo()
		}
		nodeInfoMap[nodeName].AddPod(pod)
	}
	for _, node := range nodes {
		if _, ok := nodeInfoMap[node.Name]; !ok {
			nodeInfoMap[node.Name] = framework.NewNodeInfo()
		}
		nodeInfoMap[node.Name].SetNode(node)
	}

	for _, v := range nodeInfoMap {
		nodeInfos = append(nodeInfos, v)
	}

	return &testSharedLister{
		nodes:       nodes,
		nodeInfos:   nodeInfos,
		nodeInfoMap: nodeInfoMap,
	}
}
