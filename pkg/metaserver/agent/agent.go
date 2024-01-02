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

// Package agent is the package that contains those implementations to
// obtain metadata in the specific node, any other component wants to get
// those data should import this package rather than get directly.
package agent // import "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"

import (
	"context"
	"fmt"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/cnc"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/cnr"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/kubeletconfig"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/node"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// ObjectFetcher is used to get object information.
type ObjectFetcher interface {
	// GetUnstructured returns those latest cUnstructured
	GetUnstructured(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error)
}

// MetaAgent contains all those implementations for metadata running in this agent.
type MetaAgent struct {
	start bool
	sync.Mutex

	// those fetchers provide a dynamic way to collect meta info;
	// actually, those fetchers call be set by self-defined implementations
	pod.PodFetcher
	node.NodeFetcher
	types.MetricsFetcher
	cnr.CNRFetcher
	cnc.CNCFetcher
	kubeletconfig.KubeletConfigFetcher

	// ObjectFetchers provide a way to expand fetcher for objects
	ObjectFetchers sync.Map

	// machine info is fetched from once and stored in meta-server
	*machine.KatalystMachineInfo

	Conf *config.Configuration
}

// NewMetaAgent returns the instance of MetaAgent.
func NewMetaAgent(conf *config.Configuration, clientSet *client.GenericClientSet, emitter metrics.MetricEmitter) (*MetaAgent, error) {
	podFetcher, err := pod.NewPodFetcher(conf, emitter)
	if err != nil {
		return nil, err
	}

	machineInfo, err := machine.GetKatalystMachineInfo(conf.BaseConfiguration.MachineInfoConfiguration)
	if err != nil {
		return nil, err
	}

	metaAgent := &MetaAgent{
		start:                false,
		PodFetcher:           podFetcher,
		NodeFetcher:          node.NewRemoteNodeFetcher(conf.NodeName, clientSet.KubeClient.CoreV1().Nodes()),
		CNRFetcher:           cnr.NewCachedCNRFetcher(conf.NodeName, conf.CNRCacheTTL, clientSet.InternalClient.NodeV1alpha1().CustomNodeResources()),
		KubeletConfigFetcher: kubeletconfig.NewKubeletConfigFetcher(conf, emitter),
		KatalystMachineInfo:  machineInfo,
		Conf:                 conf,
	}

	if conf.EnableMetricsFetcher {
		metaAgent.MetricsFetcher = metric.NewMetricsFetcher(emitter, metaAgent, conf)
	} else {
		metaAgent.MetricsFetcher = metric.NewFakeMetricsFetcher(emitter)
	}

	if conf.EnableCNCFetcher {
		metaAgent.CNCFetcher = cnc.NewCachedCNCFetcher(conf.NodeName, conf.CustomNodeConfigCacheTTL,
			clientSet.InternalClient.ConfigV1alpha1().CustomNodeConfigs())
	} else {
		metaAgent.CNCFetcher = cnc.NewFakeCNCFetcher(conf.NodeName, conf.CustomNodeConfigCacheTTL,
			clientSet.InternalClient.ConfigV1alpha1().CustomNodeConfigs())
	}

	return metaAgent, nil
}

func (a *MetaAgent) SetPodFetcher(p pod.PodFetcher) {
	a.setComponentImplementation(func() {
		a.PodFetcher = p
	})
}

func (a *MetaAgent) SetNodeFetcher(n node.NodeFetcher) {
	a.setComponentImplementation(func() {
		a.NodeFetcher = n
	})
}

func (a *MetaAgent) SetMetricFetcher(m types.MetricsFetcher) {
	a.setComponentImplementation(func() {
		a.MetricsFetcher = m
	})
}

func (a *MetaAgent) SetCNRFetcher(c cnr.CNRFetcher) {
	a.setComponentImplementation(func() {
		a.CNRFetcher = c
	})
}

func (a *MetaAgent) SetCNCFetcher(c cnc.CNCFetcher) {
	a.setComponentImplementation(func() {
		a.CNCFetcher = c
	})
}

func (a *MetaAgent) SetObjectFetcher(gvr metav1.GroupVersionResource, f ObjectFetcher) {
	a.ObjectFetchers.Store(gvr, f)
}

func (a *MetaAgent) GetUnstructured(ctx context.Context, gvr metav1.GroupVersionResource,
	namespace, name string) (*unstructured.Unstructured, error) {
	f, ok := a.ObjectFetchers.Load(gvr)
	if !ok {
		return nil, fmt.Errorf("gvr %v not exist", gvr)
	}
	return f.(ObjectFetcher).GetUnstructured(ctx, namespace, name)
}

func (a *MetaAgent) SetKubeletConfigFetcher(k kubeletconfig.KubeletConfigFetcher) {
	a.setComponentImplementation(func() {
		a.KubeletConfigFetcher = k
	})
}

func (a *MetaAgent) Run(ctx context.Context) {
	a.Lock()
	if a.start {
		a.Unlock()
		return
	}
	a.start = true

	go a.PodFetcher.Run(ctx)
	go a.NodeFetcher.Run(ctx)

	if a.Conf.EnableMetricsFetcher {
		go a.MetricsFetcher.Run(ctx)
	}

	a.Unlock()
	<-ctx.Done()
}

func (a *MetaAgent) setComponentImplementation(setter func()) {
	a.Lock()
	defer a.Unlock()
	if a.start {
		klog.Warningf("meta agent has already started, not allowed to set implementations")
		return
	}

	setter()
}
