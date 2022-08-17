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

package kubelet

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	info "github.com/google/cadvisor/info/v1"
	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/resourcemanager/fetcher/kubelet/topology"
	"github.com/kubewharf/katalyst-core/pkg/agent/resourcemanager/fetcher/plugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/resourcemanager/fetcher/util/kubelet/podresources"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
	"github.com/kubewharf/katalyst-core/pkg/util/qos"
)

const (
	// PluginName is name of kubelet reporter plugin
	PluginName = "kubelet-reporter-plugin"
)

// kubeletPlugin implements the endpoint interface, and it's an in-tree reporter plugin
type kubeletPlugin struct {
	ctx    context.Context
	cancel context.CancelFunc

	// conf is used to indicate the file path and name for system data in the future
	// currently, it's not used todo: implement this logic
	conf *config.Configuration

	topologyStatusAdapter topology.Adapter

	// cb since kubeletPlugin needs to call updateContent whenever the topology changes,
	// it needs a corresponding callback function
	cb plugin.ListAndWatchCallback

	// notifierCh channel sent by topology adapter to trigger ListAndWatch send to
	// manager
	notifierCh chan struct{}

	mutex                       sync.Mutex
	latestReportContentResponse *v1alpha1.GetReportContentResponse

	*process.StopControl
	emitter    metrics.MetricEmitter
	metaServer *metaserver.MetaServer
}

// NewKubeletReporterPlugin creates a kubelet reporter plugin
func NewKubeletReporterPlugin(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	conf *config.Configuration, callback plugin.ListAndWatchCallback) (plugin.ReporterPlugin, error) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &kubeletPlugin{
		emitter:     emitter,
		metaServer:  metaServer,
		conf:        conf,
		notifierCh:  make(chan struct{}),
		ctx:         ctx,
		cancel:      cancel,
		cb:          callback,
		StopControl: process.NewStopControl(time.Time{}),
	}

	topologyStatusAdapter, err := topology.NewPodResourcesServerTopologyAdapter(metaServer,
		conf.PodResourcesServerEndpoints, nil,
		p.getNumaInfo, p.isPodNumaBinding, podresources.GetV1Client)
	if err != nil {
		return nil, err
	}

	p.topologyStatusAdapter = topologyStatusAdapter

	return p, nil
}

func (p *kubeletPlugin) Name() string {
	return PluginName
}

func (p *kubeletPlugin) Run(success chan<- bool) {
	err := p.topologyStatusAdapter.Run(p.ctx, p.notifierCh)
	if err != nil {
		klog.Fatalf("run topology status adapter failed")
		return
	}
	success <- true

	for {
		select {
		case <-p.notifierCh:
			resp, err := p.getReportContent(p.ctx)
			if err != nil {
				klog.Errorf("plugin %s failed to get report content with error %v", PluginName, err)
				continue
			}

			p.ListAndWatchReportContentCallback(PluginName, resp)
		case <-p.ctx.Done():
			klog.Infof("plugin %s has been stopped", PluginName)
			return
		}
	}
}

func (p *kubeletPlugin) GetReportContent(ctx context.Context) (*v1alpha1.GetReportContentResponse, error) {
	return p.getReportContent(ctx)
}

func (p *kubeletPlugin) ListAndWatchReportContentCallback(pluginName string, response *v1alpha1.GetReportContentResponse) {
	p.setCache(response)

	p.cb(pluginName, response)
}

func (p *kubeletPlugin) GetCache() *v1alpha1.GetReportContentResponse {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	return p.latestReportContentResponse
}

// Stop to cancel all context and close notifierCh
func (p *kubeletPlugin) Stop() {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	p.cancel()
	close(p.notifierCh)

	p.StopControl.Stop()
}

func (p *kubeletPlugin) setCache(resp *v1alpha1.GetReportContentResponse) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	p.latestReportContentResponse = resp
}

// getReportContent get report content from all collectors
func (p *kubeletPlugin) getReportContent(ctx context.Context) (*v1alpha1.GetReportContentResponse, error) {
	reportContent, err := p.getTopologyStatusContent(ctx)
	if err != nil {
		return nil, err
	}

	return &v1alpha1.GetReportContentResponse{
		Content: reportContent,
	}, nil
}

// getTopologyStatusContent get topology status content from topologyStatusAdapter
func (p *kubeletPlugin) getTopologyStatusContent(ctx context.Context) ([]*v1alpha1.ReportContent, error) {
	topologyStatus, err := p.topologyStatusAdapter.GetNumaTopologyStatus(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "get numa topology status from adapter failed")
	}

	value, err := json.Marshal(&topologyStatus)
	if err != nil {
		return nil, errors.Wrap(err, "marshal topology status failed")
	}

	return []*v1alpha1.ReportContent{
		{
			GroupVersionKind: &util.CNRGroupVersionKind,
			Field: []*v1alpha1.ReportField{
				{
					FieldType: v1alpha1.FieldType_Status,
					FieldName: util.CNRFieldNameTopologyStatus,
					Value:     value,
				},
			},
		},
	}, nil
}

func (p *kubeletPlugin) getNumaInfo() ([]info.Node, error) {
	if p.metaServer == nil || p.metaServer.MachineInfo == nil {
		return nil, fmt.Errorf("get metaserver machine info is nil")
	}
	return p.metaServer.MachineInfo.Topology, nil
}

func (p *kubeletPlugin) isPodNumaBinding(pod *v1.Pod) bool {
	return qos.IsPodNumaBinding(p.conf.QoSConfiguration, pod)
}
