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

package node

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	cliflag "k8s.io/component-base/cli/flag"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/apis/overcommit/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-controller/app/options"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

func makeNoc(name string, cpuOvercommitRatio, memoryOvercommitRatio string) *v1alpha1.NodeOvercommitConfig {
	return &v1alpha1.NodeOvercommitConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			CreationTimestamp: metav1.NewTime(time.Now()),
		},
		Spec: v1alpha1.NodeOvercommitConfigSpec{
			ResourceOvercommitRatio: map[corev1.ResourceName]string{
				corev1.ResourceCPU:    cpuOvercommitRatio,
				corev1.ResourceMemory: memoryOvercommitRatio,
			},
		},
	}
}

func makeSelectorNoc(name string, cpuOvercommitRatio, memoryOvercommitRatio string, value string) *v1alpha1.NodeOvercommitConfig {
	c := makeNoc(name, cpuOvercommitRatio, memoryOvercommitRatio)
	c.Spec.NodeOvercommitSelectorVal = value
	return c
}

func makeNode(name string, labels map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
	}
}

type testCase struct {
	name          string
	initNodes     []*corev1.Node
	initConfigs   []*v1alpha1.NodeOvercommitConfig
	initCNR       []*nodev1alpha1.CustomNodeResource
	updateNodes   []*corev1.Node
	updateConfigs []*v1alpha1.NodeOvercommitConfig
	updateCNR     []*nodev1alpha1.CustomNodeResource
	addNodes      []*corev1.Node
	addConfigs    []*v1alpha1.NodeOvercommitConfig
	addCNR        []*nodev1alpha1.CustomNodeResource
	deleteNodes   []string
	deleteConfigs []string
	result        map[string]map[string]string
}

var defaultInitNodes = func() []*corev1.Node {
	return []*corev1.Node{
		makeNode("node1", map[string]string{consts.NodeOvercommitSelectorKey: "pool1"}),
		makeNode("node2", map[string]string{consts.NodeOvercommitSelectorKey: "pool2"}),
		makeNode("node3", map[string]string{consts.NodeOvercommitSelectorKey: "pool1"}),
		makeNode("node4", map[string]string{consts.NodeOvercommitSelectorKey: "pool3"}),
	}
}()

var defaultInitCNR = func() []*nodev1alpha1.CustomNodeResource {
	return []*nodev1alpha1.CustomNodeResource{
		{ObjectMeta: metav1.ObjectMeta{Name: "node1", Annotations: map[string]string{
			consts.NodeAnnotationCPUOvercommitRatioKey:    "1",
			consts.NodeAnnotationMemoryOvercommitRatioKey: "1",
		}}}, {ObjectMeta: metav1.ObjectMeta{Name: "node2", Annotations: map[string]string{
			consts.NodeAnnotationCPUOvercommitRatioKey:    "1",
			consts.NodeAnnotationMemoryOvercommitRatioKey: "1",
		}}}, {ObjectMeta: metav1.ObjectMeta{Name: "node3", Annotations: map[string]string{
			consts.NodeAnnotationCPUOvercommitRatioKey:    "3",
			consts.NodeAnnotationMemoryOvercommitRatioKey: "3",
		}}}, {ObjectMeta: metav1.ObjectMeta{Name: "node4", Annotations: map[string]string{
			consts.NodeAnnotationCPUOvercommitRatioKey:    "2",
			consts.NodeAnnotationMemoryOvercommitRatioKey: "2",
		}}},
	}
}()

var testCases = []testCase{
	{
		name:      "init: null config, add: config without selector",
		initNodes: defaultInitNodes,
		addConfigs: []*v1alpha1.NodeOvercommitConfig{
			makeNoc("config", "1", "1"),
		},
		result: map[string]map[string]string{},
	},
	{
		name:      "init: null config, add: selector config",
		initNodes: defaultInitNodes,
		addConfigs: []*v1alpha1.NodeOvercommitConfig{
			makeSelectorNoc("config-selector", "2", "1", "pool1"),
		},
		result: map[string]map[string]string{
			"node1": {consts.NodeAnnotationCPUOvercommitRatioKey: "2", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
			"node2": {consts.NodeAnnotationCPUOvercommitRatioKey: "1", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
			"node3": {consts.NodeAnnotationCPUOvercommitRatioKey: "2", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
			"node4": {consts.NodeAnnotationCPUOvercommitRatioKey: "1", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
		},
	},
	{
		name:      "update selector / nodelist config",
		initNodes: defaultInitNodes,
		initConfigs: []*v1alpha1.NodeOvercommitConfig{
			makeSelectorNoc("config-selector", "2", "1", "pool1"),
		},
		updateConfigs: []*v1alpha1.NodeOvercommitConfig{
			makeSelectorNoc("config-selector", "2", "1", "pool2"),
		},
		result: map[string]map[string]string{
			"node1": {consts.NodeAnnotationCPUOvercommitRatioKey: "1", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
			"node2": {consts.NodeAnnotationCPUOvercommitRatioKey: "2", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
			"node3": {consts.NodeAnnotationCPUOvercommitRatioKey: "1", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
			"node4": {consts.NodeAnnotationCPUOvercommitRatioKey: "1", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
		},
	},
	{
		name:      "only update config resource",
		initNodes: defaultInitNodes,
		initConfigs: []*v1alpha1.NodeOvercommitConfig{
			makeSelectorNoc("config-selector", "2", "1", "pool1"),
		},
		updateConfigs: []*v1alpha1.NodeOvercommitConfig{
			makeSelectorNoc("config-selector", "3", "1", "pool1"),
		},
		result: map[string]map[string]string{
			"node1": {consts.NodeAnnotationCPUOvercommitRatioKey: "3", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
			"node2": {consts.NodeAnnotationCPUOvercommitRatioKey: "1", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
			"node3": {consts.NodeAnnotationCPUOvercommitRatioKey: "3", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
			"node4": {consts.NodeAnnotationCPUOvercommitRatioKey: "1", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
		},
	},
	{
		name:      "update node label",
		initNodes: defaultInitNodes,
		initConfigs: []*v1alpha1.NodeOvercommitConfig{
			makeSelectorNoc("config-selector", "2", "1", "pool1"),
		},
		updateNodes: []*corev1.Node{
			makeNode("node3", map[string]string{consts.NodeOvercommitSelectorKey: "pool2"}),
			makeNode("node4", map[string]string{consts.NodeOvercommitSelectorKey: "pool1"}),
		},
		result: map[string]map[string]string{
			"node1": {consts.NodeAnnotationCPUOvercommitRatioKey: "2", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
			"node2": {consts.NodeAnnotationCPUOvercommitRatioKey: "1", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
			"node3": {consts.NodeAnnotationCPUOvercommitRatioKey: "1", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
			"node4": {consts.NodeAnnotationCPUOvercommitRatioKey: "2", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
		},
	},
	{
		name:      "delete config",
		initNodes: defaultInitNodes,
		initConfigs: []*v1alpha1.NodeOvercommitConfig{
			makeSelectorNoc("config-selector", "2", "1", "pool1"),
		},
		deleteConfigs: []string{"config-selector"},
		result: map[string]map[string]string{
			"node1": {consts.NodeAnnotationCPUOvercommitRatioKey: "1", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
			"node2": {consts.NodeAnnotationCPUOvercommitRatioKey: "1", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
			"node3": {consts.NodeAnnotationCPUOvercommitRatioKey: "1", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
			"node4": {consts.NodeAnnotationCPUOvercommitRatioKey: "1", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
		},
	},
	{
		name:      "delete node",
		initNodes: defaultInitNodes,
		initConfigs: []*v1alpha1.NodeOvercommitConfig{
			makeSelectorNoc("config2-selector", "2", "1", "pool1"),
		},
		deleteNodes: []string{"node1"},
		result: map[string]map[string]string{
			"node2": {consts.NodeAnnotationCPUOvercommitRatioKey: "1", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
			"node3": {consts.NodeAnnotationCPUOvercommitRatioKey: "2", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
			"node4": {consts.NodeAnnotationCPUOvercommitRatioKey: "1", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
		},
	},
	{
		name:      "init: node and cnr, add config",
		initNodes: defaultInitNodes,
		initCNR:   defaultInitCNR,
		addConfigs: []*v1alpha1.NodeOvercommitConfig{
			makeSelectorNoc("config-selector", "2", "2", "pool1"),
		},
		result: map[string]map[string]string{
			"node1": {consts.NodeAnnotationCPUOvercommitRatioKey: "1", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
			"node2": {consts.NodeAnnotationCPUOvercommitRatioKey: "1", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
			"node3": {consts.NodeAnnotationCPUOvercommitRatioKey: "2", consts.NodeAnnotationMemoryOvercommitRatioKey: "2"},
			"node4": {consts.NodeAnnotationCPUOvercommitRatioKey: "1", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
		},
	},
	{
		name:      "add CNR",
		initNodes: defaultInitNodes,
		initConfigs: []*v1alpha1.NodeOvercommitConfig{
			makeSelectorNoc("config-selector", "2", "2", "pool1"),
		},
		addCNR: defaultInitCNR,
		result: map[string]map[string]string{
			"node1": {consts.NodeAnnotationCPUOvercommitRatioKey: "1", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
			"node2": {consts.NodeAnnotationCPUOvercommitRatioKey: "1", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
			"node3": {consts.NodeAnnotationCPUOvercommitRatioKey: "2", consts.NodeAnnotationMemoryOvercommitRatioKey: "2"},
			"node4": {consts.NodeAnnotationCPUOvercommitRatioKey: "1", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
		},
	},
	{
		name:      "update CNR",
		initNodes: defaultInitNodes,
		initCNR:   defaultInitCNR,
		initConfigs: []*v1alpha1.NodeOvercommitConfig{
			makeSelectorNoc("config-selector", "2", "2", "pool1"),
		},
		updateCNR: []*nodev1alpha1.CustomNodeResource{
			{ObjectMeta: metav1.ObjectMeta{Name: "node3", Annotations: map[string]string{
				consts.NodeAnnotationCPUOvercommitRatioKey:    "1.5",
				consts.NodeAnnotationMemoryOvercommitRatioKey: "1.5",
			}}},
			{ObjectMeta: metav1.ObjectMeta{Name: "node1", Annotations: map[string]string{
				consts.NodeAnnotationCPUOvercommitRatioKey:    "3",
				consts.NodeAnnotationMemoryOvercommitRatioKey: "3",
			}}},
		},
		result: map[string]map[string]string{
			"node1": {consts.NodeAnnotationCPUOvercommitRatioKey: "2", consts.NodeAnnotationMemoryOvercommitRatioKey: "2"},
			"node2": {consts.NodeAnnotationCPUOvercommitRatioKey: "1", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
			"node3": {consts.NodeAnnotationCPUOvercommitRatioKey: "1.5", consts.NodeAnnotationMemoryOvercommitRatioKey: "1.5"},
			"node4": {consts.NodeAnnotationCPUOvercommitRatioKey: "1", consts.NodeAnnotationMemoryOvercommitRatioKey: "1"},
		},
	},
}

func TestReconcile(t *testing.T) {
	// test noc and node add/update/delete by reconcile
	t.Parallel()

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fss := &cliflag.NamedFlagSets{}
			nocOptions := options.NewOvercommitOptions()
			nocOptions.AddFlags(fss)
			nocOptions.ConfigReconcilePeriod = 5 * time.Second
			nocConf := controller.NewOvercommitConfig()
			_ = nocOptions.ApplyTo(nocConf)

			genericConf := &generic.GenericConfiguration{}

			controlCtx, err := katalyst_base.GenerateFakeGenericContext()
			assert.NoError(t, err)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			nocController, err := NewNodeOvercommitController(ctx, controlCtx, genericConf, nocConf)
			assert.NoError(t, err)

			for _, node := range tc.initNodes {
				_, err = controlCtx.Client.KubeClient.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{})
				assert.NoError(t, err)
			}
			for _, cnr := range tc.initCNR {
				_, err = controlCtx.Client.InternalClient.NodeV1alpha1().CustomNodeResources().Create(ctx, cnr, metav1.CreateOptions{})
				assert.NoError(t, err)
			}
			for _, config := range tc.initConfigs {
				_, err = controlCtx.Client.InternalClient.OvercommitV1alpha1().NodeOvercommitConfigs().Create(ctx, config, metav1.CreateOptions{})
				assert.NoError(t, err)
			}

			controlCtx.StartInformer(nocController.ctx)
			syncd := cache.WaitForCacheSync(nocController.ctx.Done(), nocController.syncedFunc...)
			assert.True(t, syncd)
			assert.Equal(t, 5*time.Second, nocController.reconcilePeriod)
			err = nocController.matcher.Reconcile()
			assert.NoError(t, err)

			nocController.reconcile()

			for _, node := range tc.addNodes {
				_, err = controlCtx.Client.KubeClient.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{})
				assert.NoError(t, err)
			}
			for _, cnr := range tc.addCNR {
				_, err = controlCtx.Client.InternalClient.NodeV1alpha1().CustomNodeResources().Create(ctx, cnr, metav1.CreateOptions{})
				assert.NoError(t, err)
			}
			for _, config := range tc.addConfigs {
				_, err = controlCtx.Client.InternalClient.OvercommitV1alpha1().NodeOvercommitConfigs().Create(ctx, config, metav1.CreateOptions{})
				assert.NoError(t, err)
			}
			for _, node := range tc.updateNodes {
				_, err = controlCtx.Client.KubeClient.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
				assert.NoError(t, err)
			}
			for _, config := range tc.updateConfigs {
				_, err = controlCtx.Client.InternalClient.OvercommitV1alpha1().NodeOvercommitConfigs().Update(ctx, config, metav1.UpdateOptions{})
				assert.NoError(t, err)
			}
			for _, cnr := range tc.updateCNR {
				_, err = controlCtx.Client.InternalClient.NodeV1alpha1().CustomNodeResources().Update(ctx, cnr, metav1.UpdateOptions{})
				assert.NoError(t, err)
			}
			for _, node := range tc.deleteNodes {
				err = controlCtx.Client.KubeClient.CoreV1().Nodes().Delete(ctx, node, metav1.DeleteOptions{})
				assert.NoError(t, err)
			}
			for _, config := range tc.deleteConfigs {
				err = controlCtx.Client.InternalClient.OvercommitV1alpha1().NodeOvercommitConfigs().Delete(ctx, config, metav1.DeleteOptions{})
				assert.NoError(t, err)
			}
			time.Sleep(6 * time.Second)

			for nodeName, annotations := range tc.result {
				for k, v := range annotations {
					node, err := nocController.nodeLister.Get(nodeName)
					assert.NoError(t, err)
					assert.Equal(t, v, node.Annotations[k], fmt.Sprintf("node: %v, k: %v, v: %v, actual: %v", nodeName, k, v, node.Annotations[k]))
				}
			}
		})
	}
}

func TestRun(t *testing.T) {
	t.Parallel()

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fss := &cliflag.NamedFlagSets{}
			nocOptions := options.NewOvercommitOptions()
			nocOptions.AddFlags(fss)
			nocConf := controller.NewOvercommitConfig()
			_ = nocOptions.ApplyTo(nocConf)

			genericConf := &generic.GenericConfiguration{}

			controlCtx, err := katalyst_base.GenerateFakeGenericContext()
			assert.NoError(t, err)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			nocController, err := NewNodeOvercommitController(ctx, controlCtx, genericConf, nocConf)
			assert.NoError(t, err)

			for _, node := range tc.initNodes {
				_, err = controlCtx.Client.KubeClient.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{})
				assert.NoError(t, err)
			}
			for _, cnr := range tc.initCNR {
				_, err = controlCtx.Client.InternalClient.NodeV1alpha1().CustomNodeResources().Create(ctx, cnr, metav1.CreateOptions{})
				assert.NoError(t, err)
			}
			for _, config := range tc.initConfigs {
				_, err = controlCtx.Client.InternalClient.OvercommitV1alpha1().NodeOvercommitConfigs().Create(ctx, config, metav1.CreateOptions{})
				assert.NoError(t, err)
			}

			controlCtx.StartInformer(nocController.ctx)

			go nocController.Run()
			time.Sleep(1 * time.Second)
			for _, node := range tc.addNodes {
				_, err = controlCtx.Client.KubeClient.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{})
				assert.NoError(t, err)
			}
			for _, config := range tc.addConfigs {
				_, err = controlCtx.Client.InternalClient.OvercommitV1alpha1().NodeOvercommitConfigs().Create(ctx, config, metav1.CreateOptions{})
				assert.NoError(t, err)
			}
			for _, cnr := range tc.addCNR {
				_, err = controlCtx.Client.InternalClient.NodeV1alpha1().CustomNodeResources().Create(ctx, cnr, metav1.CreateOptions{})
				assert.NoError(t, err)
			}
			for _, node := range tc.updateNodes {
				_, err = controlCtx.Client.KubeClient.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
				assert.NoError(t, err)
			}
			for _, config := range tc.updateConfigs {
				_, err = controlCtx.Client.InternalClient.OvercommitV1alpha1().NodeOvercommitConfigs().Update(ctx, config, metav1.UpdateOptions{})
				assert.NoError(t, err)
			}
			for _, cnr := range tc.updateCNR {
				_, err = controlCtx.Client.InternalClient.NodeV1alpha1().CustomNodeResources().Update(ctx, cnr, metav1.UpdateOptions{})
				assert.NoError(t, err)
			}
			for _, node := range tc.deleteNodes {
				err = controlCtx.Client.KubeClient.CoreV1().Nodes().Delete(ctx, node, metav1.DeleteOptions{})
				assert.NoError(t, err)
			}
			for _, config := range tc.deleteConfigs {
				err = controlCtx.Client.InternalClient.OvercommitV1alpha1().NodeOvercommitConfigs().Delete(ctx, config, metav1.DeleteOptions{})
				assert.NoError(t, err)
			}

			time.Sleep(2 * time.Second)

			for nodeName, annotations := range tc.result {
				for k, v := range annotations {
					node, err := nocController.nodeLister.Get(nodeName)
					assert.NoError(t, err)
					if v == "1" {
						if _, ok := node.Annotations[k]; ok {
							assert.Equal(t, v, node.Annotations[k])
						}
					}
					assert.Equal(t, v, node.Annotations[k], fmt.Sprintf("node: %v, k: %v, v: %v, actual: %v", nodeName, k, v, node.Annotations[k]))
				}
			}
		})
	}
}
