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

package reporter

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"

	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"
	"k8s.io/kubernetes/pkg/features"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-api/pkg/plugins/registration"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	overcommitRatioReporterPluginName = "overcommitratio-reporter-plugin"
)

type OvercommitManager interface {
	GetOvercommitRatio() (map[v1.ResourceName]float64, error)
}

// OvercommitRatioReporter report node overcommitment ratio calculated by agent
type OvercommitRatioReporter interface {
	Run(ctx context.Context)
}

type overcommitRatioReporterImpl struct {
	skeleton.GenericPlugin
}

func NewOvercommitRatioReporter(
	emitter metrics.MetricEmitter,
	conf *config.Configuration,
	manager OvercommitManager,
	metaServer *metaserver.MetaServer,
) (OvercommitRatioReporter, error) {
	plugin, err := newOvercommitRatioReporterPlugin(emitter, conf, manager, metaServer)
	if err != nil {
		return nil, fmt.Errorf("[overcommit-reporter] create reporter failed: %v", err)
	}

	return &overcommitRatioReporterImpl{plugin}, nil
}

func (o *overcommitRatioReporterImpl) Run(ctx context.Context) {
	if err := o.Start(); err != nil {
		klog.Fatalf("[overcommit-reporter] start %v failed: %v", o.Name(), err)
	}
	klog.Infof("[overcommit-reporter] plugin wrapper %v started", o.Name())

	<-ctx.Done()
	if err := o.Stop(); err != nil {
		klog.Errorf("[overcommit-reporter] stop %v failed: %v", o.Name(), err)
	}
	klog.Infof("[overcommit-reporter] stop")
}

type OvercommitRatioReporterPlugin struct {
	sync.Mutex

	manager    OvercommitManager
	metaServer *metaserver.MetaServer

	ctx     context.Context
	cancel  context.CancelFunc
	started bool
}

func newOvercommitRatioReporterPlugin(
	emitter metrics.MetricEmitter,
	conf *config.Configuration,
	overcommitManager OvercommitManager,
	metaserver *metaserver.MetaServer,
) (skeleton.GenericPlugin, error) {
	reporter := &OvercommitRatioReporterPlugin{
		manager:    overcommitManager,
		metaServer: metaserver,
	}

	return skeleton.NewRegistrationPluginWrapper(reporter, []string{conf.PluginRegistrationDir},
		func(key string, value int64) {
			_ = emitter.StoreInt64(key, value, metrics.MetricTypeNameCount, metrics.ConvertMapToTags(map[string]string{
				"pluginName": overcommitRatioReporterPluginName,
				"pluginType": registration.ReporterPlugin,
			})...)
		})
}

func (o *OvercommitRatioReporterPlugin) Name() string {
	return overcommitRatioReporterPluginName
}

func (o *OvercommitRatioReporterPlugin) Start() (err error) {
	o.Lock()
	defer func() {
		if err == nil {
			o.started = true
		}
		o.Unlock()
	}()

	if o.started {
		return
	}

	o.ctx, o.cancel = context.WithCancel(context.Background())
	return
}

func (o *OvercommitRatioReporterPlugin) Stop() error {
	o.Lock()
	defer func() {
		o.started = false
		o.Unlock()
	}()

	if !o.started {
		return nil
	}

	o.cancel()
	return nil
}

// GetReportContent get overcommitment ratio from manager directly.
// Since the metrics collected by Manager are already an average within a time period,
// we expect a faster response to node load fluctuations to avoid excessive overcommit of online resources.
func (o *OvercommitRatioReporterPlugin) GetReportContent(_ context.Context, _ *v1alpha1.Empty) (*v1alpha1.GetReportContentResponse, error) {
	response := &v1alpha1.GetReportContentResponse{
		Content: []*v1alpha1.ReportContent{},
	}

	overcommitRatioMap, err := o.manager.GetOvercommitRatio()
	if err != nil {
		klog.Errorf("OvercommitRatioReporterPlugin GetOvercommitMent fail: %v", err)
		return nil, err
	}
	klog.V(6).Infof("reporter get overcommit ratio: %v", overcommitRatioMap)

	// overcommit data to CNR data
	overcommitRatioContent, err := o.overcommitRatioToCNRAnnotation(overcommitRatioMap)
	if err != nil {
		return nil, err
	}
	response.Content = append(response.Content, overcommitRatioContent)

	// get topologyProvider and guaranteed cpus
	topologyProviderContent, err := o.getTopologyProviderReportContent(o.ctx)
	if err != nil {
		return nil, err
	}
	response.Content = append(response.Content, topologyProviderContent)

	return response, nil
}

func (o *OvercommitRatioReporterPlugin) ListAndWatchReportContent(_ *v1alpha1.Empty, server v1alpha1.ReporterPlugin_ListAndWatchReportContentServer) error {
	for {
		select {
		case <-o.ctx.Done():
			return nil
		case <-server.Context().Done():
			return nil
		}
	}
}

func (o *OvercommitRatioReporterPlugin) overcommitRatioToCNRAnnotation(overcommitRatioMap map[v1.ResourceName]float64) (*v1alpha1.ReportContent, error) {
	if len(overcommitRatioMap) <= 0 {
		return nil, nil
	}

	klog.V(6).Infof("overcommitRatioMap: %v", overcommitRatioMap)
	overcommitRatioAnnotation := map[string]string{}
	for resource := range overcommitRatioMap {
		switch resource {
		case v1.ResourceCPU, consts.NodeAnnotationCPUOvercommitRatioKey:
			overcommitRatioAnnotation[consts.NodeAnnotationCPUOvercommitRatioKey] = fmt.Sprintf("%.2f", overcommitRatioMap[resource])
		case v1.ResourceMemory, consts.NodeAnnotationMemoryOvercommitRatioKey:
			overcommitRatioAnnotation[consts.NodeAnnotationMemoryOvercommitRatioKey] = fmt.Sprintf("%.2f", overcommitRatioMap[resource])
		default:
			klog.Warningf("unknown resource: %v", resource.String())
		}
	}

	value, err := json.Marshal(&overcommitRatioAnnotation)
	if err != nil {
		return nil, err
	}

	return &v1alpha1.ReportContent{
		GroupVersionKind: &util.CNRGroupVersionKind,
		Field: []*v1alpha1.ReportField{
			{
				FieldType: v1alpha1.FieldType_Metadata,
				FieldName: util.CNRFieldNameAnnotations,
				Value:     value,
			},
		},
	}, nil
}

func (o *OvercommitRatioReporterPlugin) getTopologyProviderReportContent(ctx context.Context) (*v1alpha1.ReportContent, error) {
	annotations, err := o.getTopologyProvider(ctx)
	if err != nil {
		return nil, fmt.Errorf("get topology provider from adapter failed: %v", err)
	}

	guaranteedCPUs := "0"
	if annotations[consts.KCNRAnnotationCPUManager] != string(consts.CPUManagerPolicyNone) {
		guaranteedCPUs, err = o.getGuaranteedCPUs(ctx)
		if err != nil {
			return nil, err
		}
	}
	annotations[consts.KCNRAnnotationGuaranteedCPUs] = guaranteedCPUs

	value, err := json.Marshal(&annotations)
	if err != nil {
		return nil, errors.Wrap(err, "marshal topology provider failed")
	}

	return &v1alpha1.ReportContent{
		GroupVersionKind: &util.CNRGroupVersionKind,
		Field: []*v1alpha1.ReportField{
			{
				FieldType: v1alpha1.FieldType_Metadata,
				FieldName: util.CNRFieldNameAnnotations,
				Value:     value,
			},
		},
	}, nil
}

func (o *OvercommitRatioReporterPlugin) getTopologyProvider(ctx context.Context) (map[string]string, error) {
	klConfig, err := o.metaServer.GetKubeletConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("get kubelet config fail: %v", err)
	}

	return generateProviderPolicies(klConfig), nil
}

func (o *OvercommitRatioReporterPlugin) getGuaranteedCPUs(ctx context.Context) (string, error) {
	podList, err := o.metaServer.GetPodList(ctx, func(pod *v1.Pod) bool {
		return true
	})
	if err != nil {
		return "", errors.Wrap(err, "get pod list from metaserver failed")
	}

	cpus := 0
	for _, pod := range podList {
		cpus += native.PodGuaranteedCPUs(pod)
	}

	return strconv.Itoa(cpus), nil
}

func generateProviderPolicies(kubeletConfig *kubeletconfigv1beta1.KubeletConfiguration) map[string]string {
	klog.V(5).Infof("generateProviderPolicies featureGates: %v, cpuManagerPolicy: %v, memoryManagerPolicy: %v",
		kubeletConfig.FeatureGates, features.CPUManager, features.MemoryManager)

	featureGates := kubeletConfig.FeatureGates

	res := map[string]string{
		consts.KCNRAnnotationCPUManager:    string(consts.CPUManagerOff),
		consts.KCNRAnnotationMemoryManager: string(consts.MemoryManagerOff),
	}

	on, ok := featureGates[string(features.CPUManager)]
	// default true
	if (ok && on) || (!ok) {
		if kubeletConfig.CPUManagerPolicy != "" {
			res[consts.KCNRAnnotationCPUManager] = kubeletConfig.CPUManagerPolicy
		}
	}

	on, ok = featureGates[string(features.MemoryManager)]
	if (ok && on) || (!ok) {
		if kubeletConfig.MemoryManagerPolicy != "" {
			res[consts.KCNRAnnotationMemoryManager] = kubeletConfig.MemoryManagerPolicy
		}
	}

	return res
}
