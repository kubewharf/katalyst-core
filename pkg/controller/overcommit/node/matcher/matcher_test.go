/*
Copyright 2022 The Katalyst Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package matcher

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v12 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/kubewharf/katalyst-api/pkg/apis/overcommit/v1alpha1"
	v1alpha12 "github.com/kubewharf/katalyst-api/pkg/client/listers/overcommit/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
)

func fakeIndexer() cache.Indexer {
	return cache.NewIndexer(func(obj interface{}) (string, error) {
		noc, ok := obj.(*v1alpha1.NodeOvercommitConfig)
		if !ok {
			return "", nil
		}
		return noc.Name, nil
	}, cache.Indexers{
		LabelSelectorValIndex: func(obj interface{}) ([]string, error) {
			noc, ok := obj.(*v1alpha1.NodeOvercommitConfig)
			if !ok {
				return []string{}, nil
			}
			return []string{noc.Spec.NodeOvercommitSelectorVal}, nil
		},
	})
}

func fakeNodeIndexer() cache.Indexer {
	return cache.NewIndexer(func(obj interface{}) (string, error) {
		node, ok := obj.(*v1.Node)
		if !ok {
			return "", nil
		}
		return node.Name, nil
	}, cache.Indexers{
		"test": func(obj interface{}) ([]string, error) {
			return []string{}, nil
		},
	})
}

func makeTestMatcher() *MatcherImpl {
	nodeIndexer := testNodeIndexer()
	indexer := testNocIndexer()
	nocLister := v1alpha12.NewNodeOvercommitConfigLister(indexer)
	nodeLister := v12.NewNodeLister(nodeIndexer)

	return NewMatcher(nodeLister, nocLister, indexer)
}

func makeInitedMatcher() (*MatcherImpl, error) {
	m := makeTestMatcher()
	err := m.Reconcile()
	return m, err
}

func testNodeIndexer() cache.Indexer {
	indexer := fakeNodeIndexer()

	indexer.Add(&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", UID: "01", Labels: map[string]string{consts.NodeOvercommitSelectorKey: "pool1"}}})
	indexer.Add(&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node2", UID: "02", Labels: map[string]string{consts.NodeOvercommitSelectorKey: "pool2"}}})
	indexer.Add(&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node3", UID: "03", Labels: map[string]string{consts.NodeOvercommitSelectorKey: "pool1"}}})
	indexer.Add(&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node4", UID: "04", Labels: map[string]string{consts.NodeOvercommitSelectorKey: "pool3"}}})
	indexer.Add(&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node5", UID: "05"}})
	indexer.Add(&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node6", UID: "06", Labels: map[string]string{"pool": "pool1"}}})
	return indexer
}

func testNocIndexer() cache.Indexer {
	indexer := fakeIndexer()

	indexer.Add(&v1alpha1.NodeOvercommitConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "config1",
		},
		Spec: v1alpha1.NodeOvercommitConfigSpec{
			NodeOvercommitSelectorVal: "pool1",
			ResourceOvercommitRatio: map[v1.ResourceName]string{
				v1.ResourceCPU:    "1.5",
				v1.ResourceMemory: "1",
			},
		},
	})

	indexer.Add(&v1alpha1.NodeOvercommitConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "config2",
		},
		Spec: v1alpha1.NodeOvercommitConfigSpec{
			NodeOvercommitSelectorVal: "pool2",
			ResourceOvercommitRatio: map[v1.ResourceName]string{
				v1.ResourceCPU:    "2",
				v1.ResourceMemory: "1",
			},
		},
	})

	return indexer
}

func makeMatcherByIndexer(nodeIndexer, nocIndexer cache.Indexer) *MatcherImpl {
	nodeLister := v12.NewNodeLister(nodeIndexer)
	nocLister := v1alpha12.NewNodeOvercommitConfigLister(nocIndexer)

	matcher := NewMatcher(nodeLister, nocLister, nocIndexer)
	_ = matcher.Reconcile()
	return matcher
}

func TestMatchConfigNameToNodes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		configName string
		result     []string
	}{
		{
			name:       "nodeList",
			configName: "config1",
			result:     []string{"node1", "node3"},
		},
		{
			name:       "default matches all nodes",
			configName: "config2",
			result:     []string{"node2"},
		},
	}

	matcher := makeTestMatcher()

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out, err := matcher.matchConfigNameToNodes(tc.configName)
			assert.Nil(t, err)
			assert.Equal(t, len(tc.result), len(out))
		})
	}
}

func TestMatchConfig(t *testing.T) {
	t.Parallel()

	nodeIndexer1 := testNodeIndexer()
	nocIndexer1 := testNocIndexer()
	matcher1 := makeMatcherByIndexer(nodeIndexer1, nocIndexer1)
	nocIndexer1.Update(
		&v1alpha1.NodeOvercommitConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name: "config2",
			},
			Spec: v1alpha1.NodeOvercommitConfigSpec{
				NodeOvercommitSelectorVal: "pool3",
				ResourceOvercommitRatio: map[v1.ResourceName]string{
					v1.ResourceCPU:    "2",
					v1.ResourceMemory: "1",
				},
			},
		})

	nodeIndexer2 := testNodeIndexer()
	nocIndexer2 := testNocIndexer()
	matcher2 := makeMatcherByIndexer(nodeIndexer2, nocIndexer2)
	nocIndexer2.Add(&v1alpha1.NodeOvercommitConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "config3",
		},
		Spec: v1alpha1.NodeOvercommitConfigSpec{
			NodeOvercommitSelectorVal: "pool3",
			ResourceOvercommitRatio: map[v1.ResourceName]string{
				v1.ResourceCPU: "3",
			},
		},
	})

	nodeIndexer3 := testNodeIndexer()
	nocIndexer3 := testNocIndexer()
	matcher3 := makeMatcherByIndexer(nodeIndexer3, nocIndexer3)
	nocIndexer3.Delete(&v1alpha1.NodeOvercommitConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "config1",
		},
		Spec: v1alpha1.NodeOvercommitConfigSpec{
			NodeOvercommitSelectorVal: "pool1",
			ResourceOvercommitRatio: map[v1.ResourceName]string{
				v1.ResourceCPU:    "1.5",
				v1.ResourceMemory: "1",
			},
		},
	})

	testCases := []struct {
		name       string
		matcher    Matcher
		configName string
		result     []string
	}{
		{
			name:       "update selector",
			matcher:    matcher1,
			configName: "config2",
			result:     []string{"node2", "node4"},
		},
		{
			name:       "add config",
			matcher:    matcher2,
			configName: "config3",
			result:     []string{"node4"},
		},
		{
			name:       "delete config",
			matcher:    matcher3,
			configName: "config1",
			result:     []string{"node1", "node3"},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out, err := tc.matcher.MatchConfig(tc.configName)
			assert.Nil(t, err)
			sort.Strings(out)
			assert.Equal(t, tc.result, out)
		})
	}
}

func TestMatchNode(t *testing.T) {
	t.Parallel()

	nodeIndexer1 := testNodeIndexer()
	nocIndexer1 := testNocIndexer()
	matcher1 := makeMatcherByIndexer(nodeIndexer1, nocIndexer1)
	nocIndexer1.Update(&v1alpha1.NodeOvercommitConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "config2",
		},
		Spec: v1alpha1.NodeOvercommitConfigSpec{
			NodeOvercommitSelectorVal: "pool3",
			ResourceOvercommitRatio: map[v1.ResourceName]string{
				v1.ResourceCPU:    "2",
				v1.ResourceMemory: "1",
			},
		},
	})

	nodeIndexer2 := testNodeIndexer()
	nocIndexer2 := testNocIndexer()
	matcher2 := makeMatcherByIndexer(nodeIndexer2, nocIndexer2)
	nocIndexer2.Add(&v1alpha1.NodeOvercommitConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "config3",
		},
		Spec: v1alpha1.NodeOvercommitConfigSpec{
			NodeOvercommitSelectorVal: "pool3",
			ResourceOvercommitRatio: map[v1.ResourceName]string{
				v1.ResourceCPU: "3",
			},
		},
	})

	nodeIndexer3 := testNodeIndexer()
	nocIndexer3 := testNocIndexer()
	matcher3 := makeMatcherByIndexer(nodeIndexer3, nocIndexer3)
	nocIndexer3.Delete(
		&v1alpha1.NodeOvercommitConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name: "config1",
			},
		},
	)

	nodeIndexer5 := testNodeIndexer()
	nocIndexer5 := testNocIndexer()
	matcher5 := makeMatcherByIndexer(nodeIndexer5, nocIndexer5)
	nodeIndexer5.Update(
		&v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node4",
				Labels: map[string]string{
					consts.NodeOvercommitSelectorKey: "pool1",
				},
			},
		})

	nodeIndexer6 := testNodeIndexer()
	nocIndexer6 := testNocIndexer()
	matcher6 := makeMatcherByIndexer(nodeIndexer6, nocIndexer6)
	nodeIndexer5.Update(
		&v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "node1",
				Labels: nil,
			},
		})

	nodeIndexer7 := testNodeIndexer()
	nocIndexer7 := testNocIndexer()
	matcher7 := makeMatcherByIndexer(nodeIndexer7, nocIndexer7)
	nodeIndexer7.Add(&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node7", UID: "07", Labels: map[string]string{consts.NodeOvercommitSelectorKey: "pool1"}}})

	oldNodeMap := map[string]string{
		"node1": "config1",
		"node2": "config2",
		"node3": "config1",
	}

	testCases := []struct {
		name       string
		matcher    Matcher
		configName string
		nodeName   string
		oldNodeMap map[string]string
		newNodeMap map[string]string
	}{
		{
			name:       "update selector",
			matcher:    matcher1,
			configName: "config2",
			nodeName:   "",
			oldNodeMap: oldNodeMap,
			newNodeMap: map[string]string{
				"node1": "config1",
				"node3": "config1",
				"node4": "config2",
			},
		},
		{
			name:       "add config",
			matcher:    matcher2,
			configName: "config3",
			nodeName:   "",
			oldNodeMap: oldNodeMap,
			newNodeMap: map[string]string{
				"node1": "config1",
				"node2": "config2",
				"node3": "config1",
				"node4": "config3",
			},
		},
		{
			name:       "delete config",
			matcher:    matcher3,
			configName: "config1",
			nodeName:   "",
			oldNodeMap: oldNodeMap,
			newNodeMap: map[string]string{
				"node2": "config2",
			},
		},
		{
			name:       "update node label",
			matcher:    matcher5,
			configName: "",
			nodeName:   "node4",
			oldNodeMap: oldNodeMap,
			newNodeMap: map[string]string{
				"node1": "config1",
				"node2": "config2",
				"node3": "config1",
				"node4": "config1",
			},
		},
		{
			name:       "update node label2",
			matcher:    matcher6,
			configName: "",
			nodeName:   "node1",
			oldNodeMap: oldNodeMap,
			newNodeMap: map[string]string{
				"node2": "config2",
				"node3": "config1",
			},
		},
		{
			name:       "add node",
			matcher:    matcher7,
			configName: "",
			nodeName:   "node7",
			oldNodeMap: oldNodeMap,
			newNodeMap: map[string]string{
				"node1": "config1",
				"node2": "config2",
				"node3": "config1",
				"node7": "config1",
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if tc.configName == "" {
				for _, c := range []string{"config1", "config2"} {
					_, _ = tc.matcher.MatchConfig(c)
				}
				tc.matcher.MatchNode(tc.nodeName)
			} else {
				nodes, _ := tc.matcher.MatchConfig(tc.configName)
				for _, node := range nodes {
					tc.matcher.MatchNode(node)
				}
			}

			for k, v := range tc.newNodeMap {
				config := tc.matcher.GetConfig(k)
				assert.Equal(t, config.Name, v)
			}
		})
	}
}

func TestDelNode(t *testing.T) {
	t.Parallel()

	matcher1, _ := makeInitedMatcher()
	matcher2, _ := makeInitedMatcher()

	testCases := []struct {
		name     string
		nodeName string
		matcher  *MatcherImpl
		result   int
	}{
		{
			name:     "exist node",
			nodeName: "node1",
			matcher:  matcher1,
			result:   2,
		},
		{
			name:     "non-existing node",
			nodeName: "node99",
			matcher:  matcher2,
			result:   3,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.matcher.DelNode(tc.nodeName)
			fmt.Printf("testCase: %v", tc)
			assert.Equal(t, tc.result, len(tc.matcher.nodeToConfig))
		})
	}
}

func TestSort(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		nocList NocList
		result  string
	}{
		{
			name: "creationTimestamp",
			nocList: NocList{
				&v1alpha1.NodeOvercommitConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "noclist1",
						CreationTimestamp: metav1.NewTime(time.Now().Add(-10 * time.Minute)),
					},
				},
				&v1alpha1.NodeOvercommitConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "noclist2",
						CreationTimestamp: metav1.NewTime(time.Now()),
					},
				},
			},
			result: "noclist2",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sort.Sort(tc.nocList)
			assert.Equal(t, tc.result, tc.nocList[0].Name)
		})
	}
}
