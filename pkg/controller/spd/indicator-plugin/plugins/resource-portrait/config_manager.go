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

package resource_portrait

import (
	"context"
	"sync"
	"time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/klog/v2"

	apiconfig "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/scheme"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// resourcePortraitConfigManager will asynchronously synchronize the global ConfigMap configuration of the resource
// portrait to the SPD that meets the filtering conditions. This capability is controlled through a switch. If this
// capability is turned off, the user needs to write the required resource portrait configuration into the SPD.
type resourcePortraitConfigManager interface {
	Run(context.Context)
	Filter(runtime.Object) []apiconfig.ResourcePortraitConfig
}

type resourcePortraitConfigManagerImpl struct {
	sync.RWMutex
	client corev1.CoreV1Interface

	reSyncPeriod                time.Duration
	algorithmConfigMapNamespace string
	algorithmConfigMapName      string
	globalConfigs               *apiconfig.GlobalResourcePortraitConfiguration
}

func newResourcePortraitConfigManager(nn types.NamespacedName, client corev1.CoreV1Interface, reSyncPeriod time.Duration) resourcePortraitConfigManager {
	return &resourcePortraitConfigManagerImpl{
		client: client,

		reSyncPeriod:                reSyncPeriod,
		algorithmConfigMapNamespace: nn.Namespace,
		algorithmConfigMapName:      nn.Name,
	}
}

func (m *resourcePortraitConfigManagerImpl) Run(ctx context.Context) {
	go wait.Until(m.refresh, m.reSyncPeriod, ctx.Done())
}

func (m *resourcePortraitConfigManagerImpl) refresh() {
	m.Lock()
	defer m.Unlock()

	cm, err := m.client.ConfigMaps(m.algorithmConfigMapNamespace).Get(context.Background(), m.algorithmConfigMapName, v1.GetOptions{ResourceVersion: "0"})
	if err != nil {
		klog.Errorf("[spd-resource-portrait] failed to get resource portrait config: %v", err)
		return
	}

	config := &apiconfig.GlobalResourcePortraitConfiguration{}
	resourcePortraitConfig := cm.Data[ResourcePortraitPluginName]
	deserializer := scheme.Codecs.UniversalDeserializer()
	_, _, err = deserializer.Decode([]byte(resourcePortraitConfig), nil, config)
	if err != nil {
		klog.Errorf("[spd-resource-portrait] failed to parse resource portrait config: err=%v", err)
		return
	}

	m.globalConfigs = config
}

func (m *resourcePortraitConfigManagerImpl) Filter(workloadObj runtime.Object) []apiconfig.ResourcePortraitConfig {
	m.RLock()
	defer m.RUnlock()

	if m.globalConfigs == nil {
		return nil
	}

	var matchedConfigs []apiconfig.ResourcePortraitConfig
	workload := workloadObj.(*unstructured.Unstructured)
	for _, conf := range m.globalConfigs.Configs {
		if len(conf.Filter.Namespaces) > 0 && !general.SliceContains(conf.Filter.Namespaces, workload.GetNamespace()) {
			continue
		}

		if selector, err := v1.LabelSelectorAsSelector(conf.Filter.Selector); err != nil ||
			(conf.Filter.Selector != nil && !selector.Matches(labels.Set(workload.GetLabels()))) {
			continue
		}

		matchedConfigs = append(matchedConfigs, conf.Config)
	}
	return matchedConfigs
}
