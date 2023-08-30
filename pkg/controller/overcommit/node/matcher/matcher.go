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
	"context"
	"fmt"
	"sort"
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/overcommit/v1alpha1"
)

type (
	ConfigToNodes map[string]sets.String
	NodeToConfig  map[string][]*v1alpha1.NodeOvercommitConfig
)

type Matcher interface {
	// Reconcile rematch all configs and nodes
	Reconcile() error

	// MatchConfig matches nodes for config, return nodeNames whose matched config maybe updated.
	MatchConfig(configName string) ([]string, error)
	// MatchNode matches and sorts configs for node.
	MatchNode(nodeName string)
	// GetConfig get valid config by nodeName
	GetConfig(nodeName string) *v1alpha1.NodeOvercommitConfig
	// GetNodeToConfig get matched and sorted configList by nodeName
	GetNodeToConfig(nodeName string) []*v1alpha1.NodeOvercommitConfig
	// ListNodeToConfig list nodes with all matched configNames
	ListNodeToConfig() map[string][]string
	// GetNodes get matched nodes by configName
	GetNodes(configName string) []string
	// DelNode delete node in matcher cache
	DelNode(nodeName string)
}

type NodeIndexer interface {
	GetNode(nodeName string) (*v1.Node, error)
	ListNode(selector labels.Selector) ([]*v1.Node, error)
}

type NocIndexer interface {
	GetNoc(name string) (*v1alpha1.NodeOvercommitConfig, error)
	ListNoc() ([]*v1alpha1.NodeOvercommitConfig, error)
}

type NodeMatchEvent struct {
	NodeName   string
	NodeUpdate bool
}

type DummyMatcher struct{}

func (dm *DummyMatcher) Reconcile() error {
	return nil
}

func (dm *DummyMatcher) MatchConfig(configName string) ([]string, error) {
	return []string{}, nil
}

func (dm *DummyMatcher) MatchNode(nodeName string) {
	return
}

func (dm *DummyMatcher) GetConfig(nodeName string) *v1alpha1.NodeOvercommitConfig {
	return nil
}

func (dm *DummyMatcher) DelNode(nodeName string) {}

func (dm *DummyMatcher) GetNodeToConfig(nodeName string) []*v1alpha1.NodeOvercommitConfig {
	return nil
}

func (dm *DummyMatcher) ListNodeToConfig() map[string][]string {
	return nil
}

func (dm *DummyMatcher) GetNodes(configName string) []string {
	return nil
}

func NewMatcher(nodeIndexer NodeIndexer, nocIndexer NocIndexer) *MatcherImpl {
	return &MatcherImpl{
		RWMutex:       sync.RWMutex{},
		nodeIndexer:   nodeIndexer,
		nocIndexer:    nocIndexer,
		configToNodes: make(map[string]sets.String),
		nodeToConfig:  make(map[string][]*v1alpha1.NodeOvercommitConfig),
	}
}

type MatcherImpl struct {
	ctx context.Context
	sync.RWMutex

	nodeIndexer NodeIndexer
	nocIndexer  NocIndexer

	configToNodes ConfigToNodes
	nodeToConfig  NodeToConfig
}

func (i *MatcherImpl) Reconcile() error {
	err := i.reconcileConfig()
	if err != nil {
		return err
	}

	return i.reconcoleNode()
}

func (i *MatcherImpl) reconcileConfig() error {
	nodeOvercommitConfigs, err := i.nocIndexer.ListNoc()
	if err != nil {
		klog.Errorf("list nodeOvercommitConfig fail: %v", err)
		return err
	}

	i.Lock()
	defer i.Unlock()
	i.configToNodes = make(map[string]sets.String)
	for _, config := range nodeOvercommitConfigs {
		nodeNames, err := i.matchConfigToNodes(config)
		if err != nil {
			return err
		}

		i.configToNodes[config.Name] = sets.NewString(nodeNames...)
	}
	return nil
}

func (i *MatcherImpl) reconcoleNode() error {
	nodeList, err := i.nodeIndexer.ListNode(labels.Everything())
	if err != nil {
		klog.Errorf("list node fail: %v", err)
		return err
	}
	nodeOvercommitConfigs, err := i.nocIndexer.ListNoc()
	if err != nil {
		klog.Errorf("list nodeOvercommitConfig fail: %v", err)
		return err
	}

	i.Lock()
	defer i.Unlock()

	for _, node := range nodeList {
		configList := NocList{}
		for _, config := range nodeOvercommitConfigs {
			if i.configToNodes[config.Name].Has(node.Name) {
				configList = append(configList, config)
			}
		}

		if configList.Len() <= 0 {
			delete(i.nodeToConfig, node.Name)
		} else {
			sort.Sort(configList)
			i.nodeToConfig[node.Name] = configList
		}
	}

	return nil
}

func (i *MatcherImpl) GetConfig(nodeName string) *v1alpha1.NodeOvercommitConfig {
	i.RLock()
	defer i.RUnlock()
	configs := i.nodeToConfig[nodeName]
	if len(configs) == 0 {
		return nil
	}
	return configs[0]
}

func (i *MatcherImpl) MatchConfig(configName string) ([]string, error) {
	var nodeUnionSets sets.String
	nodeNames, err := i.matchConfigNameToNodes(configName)
	if err != nil && errors.IsNotFound(err) {
		i.Lock()
		nodeUnionSets = i.configToNodes[configName]
		delete(i.configToNodes, configName)
		i.Unlock()
		return nodeUnionSets.UnsortedList(), nil
	}
	if err != nil {
		return nil, err
	}
	i.Lock()
	nodeUnionSets = i.configNodeUnion(configName, nodeNames)
	i.configToNodes[configName] = sets.NewString(nodeNames...)
	i.Unlock()
	return nodeUnionSets.UnsortedList(), nil
}

func (i *MatcherImpl) MatchNode(nodeName string) {
	var (
		configList = NocList{}
		err        error
		config     *v1alpha1.NodeOvercommitConfig
	)

	i.RLock()
	for configName, nodeSet := range i.configToNodes {
		if nodeSet.Has(nodeName) {
			config, err = i.nocIndexer.GetNoc(configName)
			if err != nil {
				klog.Errorf("get nodeOvercommitConfig %s fail: %v", configName, err)
				continue
			}
			configList = append(configList, config)
		}
	}
	i.RUnlock()

	sort.Sort(configList)

	i.Lock()
	defer i.Unlock()
	if configList.Len() <= 0 {
		delete(i.nodeToConfig, nodeName)
		return
	}

	i.nodeToConfig[nodeName] = configList
	return
}

func (i *MatcherImpl) DelNode(nodeName string) {
	i.Lock()
	defer i.Unlock()

	delete(i.nodeToConfig, nodeName)
	for key := range i.configToNodes {
		i.configToNodes[key].Delete(nodeName)
	}
}

func (i *MatcherImpl) GetNodeToConfig(nodeName string) []*v1alpha1.NodeOvercommitConfig {
	i.RLock()
	defer i.RUnlock()
	return i.nodeToConfig[nodeName]
}

func (i *MatcherImpl) ListNodeToConfig() map[string][]string {
	ret := make(map[string][]string)
	i.RLock()
	defer i.RUnlock()

	for nodeName, configs := range i.nodeToConfig {
		ret[nodeName] = make([]string, 0)
		for i := range configs {
			ret[nodeName] = append(ret[nodeName], configs[i].Name)
		}
	}
	return ret
}

func (i *MatcherImpl) GetNodes(configName string) []string {
	i.RLock()
	defer i.RUnlock()
	return i.configToNodes[configName].UnsortedList()
}

func (i *MatcherImpl) matchConfigNameToNodes(configName string) ([]string, error) {
	overcommitConfig, err := i.nocIndexer.GetNoc(configName)
	if err != nil {
		return nil, err
	}

	if overcommitConfig == nil {
		return nil, fmt.Errorf("config %s is nil", configName)
	}

	return i.matchConfigToNodes(overcommitConfig)
}

func (i *MatcherImpl) matchConfigToNodes(overcommitConfig *v1alpha1.NodeOvercommitConfig) ([]string, error) {
	var (
		nodeNames  = make([]string, 0)
		configType = NodeOvercommitConfigType(overcommitConfig)
	)

	switch configType {
	case ConfigNodeList:
		for _, nodeName := range overcommitConfig.Spec.NodeList {
			nodeNames = append(nodeNames, nodeName)
		}
		return nodeNames, nil
	case ConfigSelector:
		selector, err := metav1.LabelSelectorAsSelector(overcommitConfig.Spec.Selector)
		if err != nil {
			klog.Errorf("config %s LabelSelectorAsSelector fail: %v", overcommitConfig.Name, err)
			return nil, err
		}
		nodeList, err := i.nodeIndexer.ListNode(selector)
		if err != nil {
			klog.Errorf("list node by %s fail: %v", selector.String(), err)
			return nil, err
		}
		for _, node := range nodeList {
			nodeNames = append(nodeNames, node.Name)
		}
		return nodeNames, nil
	case ConfigDefault:
		fallthrough
	default:
		nodeList, err := i.nodeIndexer.ListNode(labels.Everything())
		if err != nil {
			klog.Errorf("list all node fail: %v", err)
			return nil, err
		}
		for _, node := range nodeList {
			nodeNames = append(nodeNames, node.Name)
		}
		return nodeNames, nil
	}
}

func (i *MatcherImpl) configNodeUnion(configName string, nodeNames []string) sets.String {
	var (
		newNodes = sets.NewString(nodeNames...)
	)

	originalNodes, ok := i.configToNodes[configName]
	if !ok {
		return newNodes
	}

	return originalNodes.Union(newNodes)
}
