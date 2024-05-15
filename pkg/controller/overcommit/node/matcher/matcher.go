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
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/sets"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/overcommit/v1alpha1"
	overcommitlisters "github.com/kubewharf/katalyst-api/pkg/client/listers/overcommit/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
)

const LabelSelectorValIndex = "spec.nodeOvercommitSelectorVal"

type (
	ConfigToNodes map[string]sets.String
	NodeToConfig  map[string]*v1alpha1.NodeOvercommitConfig
)

type Matcher interface {
	// Reconcile rematch all configs and nodes
	Reconcile() error

	// MatchConfig matches nodes for config, return nodeNames whose matched config maybe updated.
	MatchConfig(configName string) ([]string, error)
	// MatchNode matches and sorts configs for node.
	MatchNode(nodeName string) (*v1alpha1.NodeOvercommitConfig, error)
	// ListNodeToConfig list nodes with matched configName
	ListNodeToConfig() map[string]string
	// GetConfig get matched config by nodeName
	GetConfig(nodeName string) *v1alpha1.NodeOvercommitConfig
	// GetNodes get matched nodes by configName
	GetNodes(configName string) []string
	// DelNode delete node in matcher cache
	DelNode(nodeName string)
}

type NodeMatchEvent struct {
	NodeName   string
	NodeUpdate bool
}

type DummyMatcher struct{}

func (dm *DummyMatcher) Reconcile() error {
	return nil
}

func (dm *DummyMatcher) MatchConfig(_ string) ([]string, error) {
	return []string{}, nil
}

func (dm *DummyMatcher) MatchNode(_ string) (*v1alpha1.NodeOvercommitConfig, error) {
	return nil, nil
}

func (dm *DummyMatcher) GetConfig(_ string) *v1alpha1.NodeOvercommitConfig {
	return nil
}

func (dm *DummyMatcher) DelNode(_ string) {}

func (dm *DummyMatcher) GetNodeToConfig(_ string) *v1alpha1.NodeOvercommitConfig {
	return nil
}

func (dm *DummyMatcher) ListNodeToConfig() map[string]string {
	return nil
}

func (dm *DummyMatcher) GetNodes(_ string) []string {
	return nil
}

func NewMatcher(nodelister corelisters.NodeLister, noclister overcommitlisters.NodeOvercommitConfigLister, indexer cache.Indexer) *MatcherImpl {
	return &MatcherImpl{
		RWMutex:       sync.RWMutex{},
		nodeLister:    nodelister,
		nocLister:     noclister,
		nocIndexer:    indexer,
		configToNodes: make(map[string]sets.String),
		nodeToConfig:  make(map[string]*v1alpha1.NodeOvercommitConfig),
	}
}

type MatcherImpl struct {
	ctx context.Context
	sync.RWMutex

	nodeLister corelisters.NodeLister
	nocLister  overcommitlisters.NodeOvercommitConfigLister

	nocIndexer    cache.Indexer
	configToNodes ConfigToNodes
	nodeToConfig  NodeToConfig
}

func (i *MatcherImpl) Reconcile() error {
	err := i.reconcileConfig()
	if err != nil {
		return err
	}

	return i.reconcileNode()
}

func (i *MatcherImpl) reconcileConfig() error {
	nodeOvercommitConfigs, err := i.nocLister.List(labels.Everything())
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

func (i *MatcherImpl) reconcileNode() error {
	nodeList, err := i.nodeLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("list node fail: %v", err)
		return err
	}

	i.Lock()
	defer i.Unlock()

	for _, node := range nodeList {
		_, err := i.matchNode(node)
		if err != nil {
			klog.Errorf("matchNode %v fail: %v", node.Name, err)
		}
	}
	return nil
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

func (i *MatcherImpl) MatchNode(nodeName string) (*v1alpha1.NodeOvercommitConfig, error) {
	node, err := i.nodeLister.Get(nodeName)
	if err != nil {
		if errors.IsNotFound(err) {
			klog.Errorf("node %v has been deleted", nodeName)
			i.Lock()
			delete(i.nodeToConfig, nodeName)
			i.Unlock()
			return nil, nil
		}
		klog.Errorf("getnode %v fail, err: %v", nodeName, err)
		return nil, err
	}

	i.Lock()
	defer i.Unlock()
	return i.matchNode(node)
}

func (i *MatcherImpl) matchNode(node *v1.Node) (*v1alpha1.NodeOvercommitConfig, error) {
	nodeName := node.Name
	if len(node.Labels) == 0 {
		delete(i.nodeToConfig, nodeName)
		return nil, nil
	}
	val, ok := node.Labels[consts.NodeOvercommitSelectorKey]
	if !ok {
		klog.Warningf("node %s has no label %s", nodeName, consts.NodeOvercommitSelectorKey)
		delete(i.nodeToConfig, nodeName)
		return nil, nil
	}
	config, err := GetValidNodeOvercommitConfig(i.nocIndexer, val)
	if err != nil {
		return nil, err
	}
	if config == nil {
		delete(i.nodeToConfig, nodeName)
		return nil, nil
	}
	i.nodeToConfig[nodeName] = config
	return config, nil
}

func (i *MatcherImpl) DelNode(nodeName string) {
	i.Lock()
	defer i.Unlock()

	delete(i.nodeToConfig, nodeName)
	for key := range i.configToNodes {
		i.configToNodes[key].Delete(nodeName)
	}
}

func (i *MatcherImpl) GetConfig(nodeName string) *v1alpha1.NodeOvercommitConfig {
	i.RLock()
	defer i.RUnlock()
	return i.nodeToConfig[nodeName]
}

func (i *MatcherImpl) ListNodeToConfig() map[string]string {
	ret := make(map[string]string)
	i.RLock()
	defer i.RUnlock()

	for nodeName, config := range i.nodeToConfig {
		ret[nodeName] = config.Name
	}
	return ret
}

func (i *MatcherImpl) GetNodes(configName string) []string {
	i.RLock()
	defer i.RUnlock()
	return i.configToNodes[configName].UnsortedList()
}

func (i *MatcherImpl) matchConfigNameToNodes(configName string) ([]string, error) {
	overcommitConfig, err := i.nocLister.Get(configName)
	if err != nil {
		return nil, err
	}

	if overcommitConfig == nil {
		return nil, fmt.Errorf("config %s is nil", configName)
	}

	return i.matchConfigToNodes(overcommitConfig)
}

func (i *MatcherImpl) matchConfigToNodes(overcommitConfig *v1alpha1.NodeOvercommitConfig) ([]string, error) {
	nodeNames := make([]string, 0)

	val := overcommitConfig.Spec.NodeOvercommitSelectorVal
	if val == "" {
		return []string{}, nil
	}

	requirement, err := labels.NewRequirement(consts.NodeOvercommitSelectorKey, selection.Equals, []string{val})
	if err != nil {
		return nil, err
	}
	selector := labels.NewSelector().Add(*requirement)

	nodeList, err := i.nodeLister.List(selector)
	if err != nil {
		return nil, err
	}
	for _, node := range nodeList {
		nodeNames = append(nodeNames, node.Name)
	}
	return nodeNames, nil
}

func (i *MatcherImpl) configNodeUnion(configName string, nodeNames []string) sets.String {
	newNodes := sets.NewString(nodeNames...)

	originalNodes, ok := i.configToNodes[configName]
	if !ok {
		return newNodes
	}

	return originalNodes.Union(newNodes)
}
