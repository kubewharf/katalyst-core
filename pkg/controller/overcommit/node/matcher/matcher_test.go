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
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/kubewharf/katalyst-api/pkg/apis/overcommit/v1alpha1"
)

type FakeNodeIndexer struct {
	nodeMap map[string]*v1.Node
}

func newFakeNodeIndexer() *FakeNodeIndexer {
	return &FakeNodeIndexer{
		nodeMap: make(map[string]*v1.Node),
	}
}

func (fn *FakeNodeIndexer) GetNode(nodeName string) (*v1.Node, error) {
	if _, ok := fn.nodeMap[nodeName]; !ok {
		return nil, errors.NewNotFound(v1.Resource("Node"), nodeName)
	}
	return fn.nodeMap[nodeName], nil
}

func (fn *FakeNodeIndexer) ListNode(selector labels.Selector) ([]*v1.Node, error) {
	nodeList := make([]*v1.Node, 0)
	for _, node := range fn.nodeMap {
		if selector.Matches(labels.Set(node.Labels)) {
			nodeList = append(nodeList, node)
		}
	}
	return nodeList, nil
}

func (fn *FakeNodeIndexer) add(node *v1.Node) {
	fn.nodeMap[node.Name] = node
}

type FakeNocIndexer struct {
	configMap map[string]*v1alpha1.NodeOvercommitConfig
}

func newFakeNocIndexer() *FakeNocIndexer {
	return &FakeNocIndexer{
		configMap: make(map[string]*v1alpha1.NodeOvercommitConfig),
	}
}

func (fn *FakeNocIndexer) GetNoc(name string) (*v1alpha1.NodeOvercommitConfig, error) {
	if _, ok := fn.configMap[name]; !ok {
		return nil, errors.NewNotFound(v1.Resource("NodeOvercommitConfig"), name)
	}
	return fn.configMap[name], nil
}

func (fn *FakeNocIndexer) ListNoc() ([]*v1alpha1.NodeOvercommitConfig, error) {
	configList := make([]*v1alpha1.NodeOvercommitConfig, 0)
	for _, c := range fn.configMap {
		configList = append(configList, c)
	}
	return configList, nil
}

func (fn *FakeNocIndexer) add(config *v1alpha1.NodeOvercommitConfig) {
	fn.configMap[config.Name] = config
}

func makeTestMatcher() *MatcherImpl {
	nodeIndexer := makeTestNodeIndexer()
	nocIndexer := makeTestNocIndexer()

	return NewMatcher(nodeIndexer, nocIndexer)
}

func makeInitedMatcher() (*MatcherImpl, error) {
	m := makeTestMatcher()
	err := m.Reconcile()
	return m, err
}

func makeTestNodeIndexer() *FakeNodeIndexer {
	nodeIndexer := newFakeNodeIndexer()

	nodeIndexer.add(&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", UID: "01", Labels: map[string]string{"pool": "pool1"}}})
	nodeIndexer.add(&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node2", UID: "02", Labels: map[string]string{"pool": "pool2"}}})
	nodeIndexer.add(&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node3", UID: "03", Labels: map[string]string{"pool": "pool1"}}})
	nodeIndexer.add(&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node4", UID: "04", Labels: map[string]string{"pool": "pool3"}}})
	return nodeIndexer
}

func makeTestNocIndexer() *FakeNocIndexer {
	nocIndexer := newFakeNocIndexer()

	nocIndexer.add(&v1alpha1.NodeOvercommitConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "config1-nodeList",
		},
		Spec: v1alpha1.NodeOvercommitConfigSpec{
			NodeList: []string{
				"node1",
				"node2",
			},
			ResourceOvercommitRatioConfig: map[v1.ResourceName]string{
				v1.ResourceCPU:    "1.5",
				v1.ResourceMemory: "1",
			},
		},
	})

	nocIndexer.add(&v1alpha1.NodeOvercommitConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "config2-default",
		},
		Spec: v1alpha1.NodeOvercommitConfigSpec{
			ResourceOvercommitRatioConfig: map[v1.ResourceName]string{
				v1.ResourceCPU:    "1",
				v1.ResourceMemory: "1",
			},
		},
	})

	nocIndexer.add(&v1alpha1.NodeOvercommitConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "config3-selector",
		},
		Spec: v1alpha1.NodeOvercommitConfigSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"pool": "pool1",
				},
			},
			ResourceOvercommitRatioConfig: map[v1.ResourceName]string{
				v1.ResourceCPU: "2",
			},
		},
	})

	return nocIndexer
}

func makeMatcherByIndexer(nodeIndexer *FakeNodeIndexer, nocIndexer *FakeNocIndexer) *MatcherImpl {
	matcher := NewMatcher(nodeIndexer, nocIndexer)
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
			configName: "config1-nodeList",
			result:     []string{"node1", "node2"},
		},
		{
			name:       "selector",
			configName: "config3-selector",
			result:     []string{"node1", "node3"},
		},
		{
			name:       "default matches all nodes",
			configName: "config2-default",
			result:     []string{"node1", "node2", "node3", "node4"},
		},
	}

	matcher := makeTestMatcher()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := matcher.matchConfigNameToNodes(tc.configName)
			assert.Nil(t, err)
			assert.Equal(t, len(tc.result), len(out))
		})
	}
}

func TestMatchConfig(t *testing.T) {
	t.Parallel()
	nodeIndexer1 := makeTestNodeIndexer()
	nocIndexer1 := makeTestNocIndexer()
	matcher1 := makeMatcherByIndexer(nodeIndexer1, nocIndexer1)
	nocIndexer1.configMap["config3-selector"].Spec.Selector.MatchLabels["pool"] = "pool2"

	nodeIndexer2 := makeTestNodeIndexer()
	nocIndexer2 := makeTestNocIndexer()
	matcher2 := makeMatcherByIndexer(nodeIndexer2, nocIndexer2)
	nocIndexer2.add(&v1alpha1.NodeOvercommitConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "config4-selector",
		},
		Spec: v1alpha1.NodeOvercommitConfigSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"pool": "pool2",
				},
			},
			ResourceOvercommitRatioConfig: map[v1.ResourceName]string{
				v1.ResourceCPU: "3",
			},
		},
	})

	nodeIndexer3 := makeTestNodeIndexer()
	nocIndexer3 := makeTestNocIndexer()
	matcher3 := makeMatcherByIndexer(nodeIndexer3, nocIndexer3)
	delete(nocIndexer3.configMap, "config3-selector")

	nodeIndexer4 := makeTestNodeIndexer()
	nocIndexer4 := makeTestNocIndexer()
	matcher4 := makeMatcherByIndexer(nodeIndexer4, nocIndexer4)
	nocIndexer4.configMap["config1-nodeList"].Spec.NodeList = nil
	nocIndexer4.configMap["config1-nodeList"].Spec.Selector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"pool": "pool2",
		},
	}

	testCases := []struct {
		name       string
		matcher    Matcher
		configName string
		result     []string
	}{
		{
			name:       "update selector",
			matcher:    matcher1,
			configName: "config3-selector",
			result:     []string{"node1", "node2", "node3"},
		},
		{
			name:       "add config",
			matcher:    matcher2,
			configName: "config4-selector",
			result:     []string{"node2"},
		},
		{
			name:       "delete config",
			matcher:    matcher3,
			configName: "config3-selector",
			result:     []string{"node1", "node3"},
		},
		{
			name:       "update config type",
			matcher:    matcher4,
			configName: "config1-nodeList",
			result:     []string{"node1", "node2"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.matcher.MatchConfig(tc.configName)
			assert.Nil(t, err)
			assert.Equal(t, len(out), len(tc.result))
		})
	}
}

func TestMatchNode(t *testing.T) {
	t.Parallel()
	// node1 -> config1-nodeList, node2 -> config1-nodeList, node3 -> config3-selector, node4 -> config2-selector

	nodeIndexer1 := makeTestNodeIndexer()
	nocIndexer1 := makeTestNocIndexer()
	matcher1 := makeMatcherByIndexer(nodeIndexer1, nocIndexer1)
	nocIndexer1.configMap["config3-selector"].Spec.Selector.MatchLabels["pool"] = "pool2"

	nodeIndexer2 := makeTestNodeIndexer()
	nocIndexer2 := makeTestNocIndexer()
	matcher2 := makeMatcherByIndexer(nodeIndexer2, nocIndexer2)
	nocIndexer2.add(&v1alpha1.NodeOvercommitConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "config4-selector",
		},
		Spec: v1alpha1.NodeOvercommitConfigSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"pool": "pool3",
				},
			},
			ResourceOvercommitRatioConfig: map[v1.ResourceName]string{
				v1.ResourceCPU: "3",
			},
		},
	})

	nodeIndexer3 := makeTestNodeIndexer()
	nocIndexer3 := makeTestNocIndexer()
	matcher3 := makeMatcherByIndexer(nodeIndexer3, nocIndexer3)
	delete(nocIndexer3.configMap, "config3-selector")

	nodeIndexer4 := makeTestNodeIndexer()
	nocIndexer4 := makeTestNocIndexer()
	matcher4 := makeMatcherByIndexer(nodeIndexer4, nocIndexer4)
	nocIndexer4.configMap["config1-nodeList"].Spec.NodeList = nil
	nocIndexer4.configMap["config1-nodeList"].Spec.Selector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"pool": "pool2",
		},
	}

	nodeIndexer5 := makeTestNodeIndexer()
	nocIndexer5 := makeTestNocIndexer()
	matcher5 := makeMatcherByIndexer(nodeIndexer5, nocIndexer5)
	nodeIndexer5.nodeMap["node4"].Labels["pool"] = "pool1"

	nodeIndexer6 := makeTestNodeIndexer()
	nocIndexer6 := makeTestNocIndexer()
	matcher6 := makeMatcherByIndexer(nodeIndexer6, nocIndexer6)
	nodeIndexer6.nodeMap["node1"].Labels = nil

	nodeIndexer7 := makeTestNodeIndexer()
	nocIndexer7 := makeTestNocIndexer()
	matcher7 := makeMatcherByIndexer(nodeIndexer7, nocIndexer7)
	nodeIndexer7.add(&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node5", UID: "05", Labels: map[string]string{"pool": "pool1"}}})

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
			configName: "config3-selector",
			nodeName:   "",
			oldNodeMap: map[string]string{
				"node1": "config1-nodeList",
				"node2": "config1-nodeList",
				"node3": "config3-selector",
				"node4": "config2-default",
			},
			newNodeMap: map[string]string{
				"node1": "config1-nodeList",
				"node2": "config1-nodeList",
				"node3": "config2-default",
				"node4": "config2-default",
			},
		},
		{
			name:       "add config",
			matcher:    matcher2,
			configName: "config4-selector",
			nodeName:   "",
			oldNodeMap: map[string]string{
				"node1": "config1-nodeList",
				"node2": "config1-nodeList",
				"node3": "config3-selector",
				"node4": "config2-default",
			},
			newNodeMap: map[string]string{
				"node1": "config1-nodeList",
				"node2": "config1-nodeList",
				"node3": "config3-selector",
				"node4": "config4-selector",
			},
		},
		{
			name:       "delete config",
			matcher:    matcher3,
			configName: "config3-selector",
			nodeName:   "",
			oldNodeMap: map[string]string{
				"node1": "config1-nodeList",
				"node2": "config1-nodeList",
				"node3": "config3-selector",
				"node4": "config2-default",
			},
			newNodeMap: map[string]string{
				"node1": "config1-nodeList",
				"node2": "config1-nodeList",
				"node3": "config2-default",
				"node4": "config2-default",
			},
		},
		{
			name:       "update config type",
			matcher:    matcher4,
			configName: "config1-nodeList",
			nodeName:   "",
			oldNodeMap: map[string]string{
				"node1": "config1-nodeList",
				"node2": "config1-nodeList",
				"node3": "config3-selector",
				"node4": "config2-default",
			},
			newNodeMap: map[string]string{
				"node1": "config3-selector",
				"node2": "config1-nodeList",
				"node3": "config3-selector",
				"node4": "config2-default",
			},
		},
		{
			name:       "update node label",
			matcher:    matcher5,
			configName: "",
			nodeName:   "node4",
			oldNodeMap: map[string]string{
				"node1": "config1-nodeList",
				"node2": "config1-nodeList",
				"node3": "config3-selector",
				"node4": "config2-default",
			},
			newNodeMap: map[string]string{
				"node1": "config1-nodeList",
				"node2": "config1-nodeList",
				"node3": "config3-selector",
				"node4": "config3-selector",
			},
		},
		{
			name:       "update node label2",
			matcher:    matcher6,
			configName: "",
			nodeName:   "node1",
			oldNodeMap: map[string]string{
				"node1": "config1-nodeList",
				"node2": "config1-nodeList",
				"node3": "config3-selector",
				"node4": "config2-default",
			},
			newNodeMap: map[string]string{
				"node1": "config1-nodeList",
				"node2": "config1-nodeList",
				"node3": "config3-selector",
				"node4": "config2-default",
			},
		},
		{
			name:       "add node",
			matcher:    matcher7,
			configName: "",
			nodeName:   "node5",
			oldNodeMap: map[string]string{
				"node1": "config1-nodeList",
				"node2": "config1-nodeList",
				"node3": "config3-selector",
				"node4": "config2-default",
			},
			newNodeMap: map[string]string{
				"node1": "config1-nodeList",
				"node2": "config1-nodeList",
				"node3": "config3-selector",
				"node4": "config2-default",
				"node5": "config3-selector",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.configName == "" {
				for _, c := range []string{"config1-nodeList", "config2-default", "config3-selector"} {
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
			result:   3,
		},
		{
			name:     "non-existing node",
			nodeName: "node99",
			matcher:  matcher2,
			result:   4,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
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
			name: "default and selector",
			nocList: NocList{
				&v1alpha1.NodeOvercommitConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "default",
					},
					Spec: v1alpha1.NodeOvercommitConfigSpec{},
				},
				&v1alpha1.NodeOvercommitConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "selector",
					},
					Spec: v1alpha1.NodeOvercommitConfigSpec{
						Selector: &metav1.LabelSelector{},
					},
				},
			},
			result: "selector",
		},
		{
			name: "selector and nodelist",
			nocList: NocList{
				&v1alpha1.NodeOvercommitConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "nodelist",
					},
					Spec: v1alpha1.NodeOvercommitConfigSpec{
						NodeList: []string{},
					},
				},
				&v1alpha1.NodeOvercommitConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "selector",
					},
					Spec: v1alpha1.NodeOvercommitConfigSpec{
						Selector: &metav1.LabelSelector{},
					},
				},
			},
			result: "nodelist",
		},
		{
			name: "creationTimestamp",
			nocList: NocList{
				&v1alpha1.NodeOvercommitConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "nodelist1",
						CreationTimestamp: metav1.NewTime(time.Now().Add(-10 * time.Minute)),
					},
					Spec: v1alpha1.NodeOvercommitConfigSpec{
						NodeList: []string{},
					},
				},
				&v1alpha1.NodeOvercommitConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "nodelist2",
						CreationTimestamp: metav1.NewTime(time.Now()),
					},
					Spec: v1alpha1.NodeOvercommitConfigSpec{
						NodeList: []string{},
					},
				},
			},
			result: "nodelist2",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sort.Sort(tc.nocList)
			assert.Equal(t, tc.result, tc.nocList[0].Name)
		})
	}
}
