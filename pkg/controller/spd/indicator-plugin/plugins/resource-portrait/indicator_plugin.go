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
	"fmt"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	metrics "k8s.io/metrics/pkg/apis/metrics/v1beta1"

	apiconfig "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	apiworkload "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	apiListers "github.com/kubewharf/katalyst-api/pkg/client/listers/workload/v1alpha1"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	indicatorplugin "github.com/kubewharf/katalyst-core/pkg/controller/spd/indicator-plugin"
	katalystmetrics "github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	spdWorkerCount = 1

	ResourcePortraitExtendedSpecName = "ResourcePortrait"
	ResourcePortraitPluginName       = "ResourcePortraitIndicatorPlugin"
)

const (
	resourcePortraitAlgorithmTimeWindowInputMin           = 60
	resourcePortraitAlgorithmTimeWindowOutputMin          = 1
	resourcePortraitAlgorithmTimeWindowHistoryStepsMin    = 1
	resourcePortraitAlgorithmTimeWindowPredictionStepsMin = 1
)

const (
	metricsNameResourcePortraitMetric = "resource_portrait_metrics"
)

type ResourcePortraitIndicatorPlugin struct {
	ctx  context.Context
	conf *controller.ResourcePortraitIndicatorPluginConfig

	spdQueue       workqueue.RateLimitingInterface
	spdLister      apiListers.ServiceProfileDescriptorLister
	workloadLister map[schema.GroupVersionResource]cache.GenericLister

	updater        indicatorplugin.IndicatorUpdater
	metricsEmitter katalystmetrics.MetricEmitter
	configManager  resourcePortraitConfigManager

	dataSourceClient DataSourceClient
}

func (p *ResourcePortraitIndicatorPlugin) Run() {
	defer utilruntime.HandleCrash()
	defer p.spdQueue.ShutDown()
	defer klog.Infof("shutting down spd plugin: %s", p.Name())

	go wait.Until(p.emitMetrics, p.conf.ReSyncPeriod, p.ctx.Done())

	if p.conf.EnableAutomaticResyncGlobalConfiguration {
		p.configManager.Run(p.ctx)
		go wait.Until(p.resyncSpecWorker, p.conf.ReSyncPeriod, p.ctx.Done())
	}

	for i := 0; i < spdWorkerCount; i++ {
		go wait.Until(p.spdWorker, p.conf.ReSyncPeriod, p.ctx.Done())
	}

	<-p.ctx.Done()
}

func (p *ResourcePortraitIndicatorPlugin) Name() string { return ResourcePortraitPluginName }
func (p *ResourcePortraitIndicatorPlugin) GetSupportedBusinessIndicatorSpec() []apiworkload.ServiceBusinessIndicatorName {
	return nil
}

func (p *ResourcePortraitIndicatorPlugin) GetSupportedSystemIndicatorSpec() []apiworkload.ServiceSystemIndicatorName {
	return nil
}

func (p *ResourcePortraitIndicatorPlugin) GetSupportedBusinessIndicatorStatus() []apiworkload.ServiceBusinessIndicatorName {
	return nil
}

func (p *ResourcePortraitIndicatorPlugin) GetSupportedExtendedIndicatorSpec() []string {
	return []string{ResourcePortraitExtendedSpecName}
}

func (p *ResourcePortraitIndicatorPlugin) GetSupportedAggMetricsStatus() []string {
	return []string{ResourcePortraitPluginName}
}

// resyncSpecWorker is used to synchronize global configuration to SPD.
func (p *ResourcePortraitIndicatorPlugin) resyncSpecWorker() {
	spdList, err := p.spdLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("[spd-resource-portrait] failed to list all spd: %v", err)
		return
	}

	for _, spd := range spdList {
		gvr, _ := meta.UnsafeGuessKindToResource(schema.FromAPIVersionAndKind(spd.Spec.TargetRef.APIVersion, spd.Spec.TargetRef.Kind))
		workloadLister, ok := p.workloadLister[gvr]
		if !ok {
			klog.Errorf("[spd-resource-portrait] spd %s without workload lister", spd.Name)
			continue
		}

		workloadObj, err := util.GetWorkloadForSPD(spd, workloadLister)
		if err != nil {
			if errors.IsNotFound(err) {
				continue
			} else {
				klog.Errorf("[spd-resource-portrait] get workload for spd %s error: %v", spd.Name, err)
			}
		} else {
			workload := workloadObj.(*unstructured.Unstructured)
			matchedConfigs := p.configManager.Filter(workload)
			if len(matchedConfigs) == 0 {
				klog.Infof("[spd-resource-portrait] spd %s without matched config", spd.Name)
				continue
			}

			p.updater.UpdateExtendedIndicatorSpec(types.NamespacedName{
				Namespace: spd.Namespace,
				Name:      spd.Name,
			}, []apiworkload.ServiceExtendedIndicatorSpec{{
				Name:       ResourcePortraitExtendedSpecName,
				Indicators: runtime.RawExtension{Object: &apiconfig.ResourcePortraitIndicators{Configs: matchedConfigs}},
			}})
		}
	}
}

func (p *ResourcePortraitIndicatorPlugin) emitMetrics() {
	spdList, err := p.spdLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("[spd-resource-portrait] failed to list all spd: %v", err)
		return
	}

	for _, spd := range spdList {
		for _, aggMetrics := range spd.Status.AggMetrics {
			if aggMetrics.Scope != p.Name() {
				continue
			}

			if len(aggMetrics.Items) == 0 {
				continue
			}

			var currentMetric *metrics.PodMetrics
			now := time.Now()
			for _, item := range aggMetrics.Items {
				if now.After(item.Timestamp.Time) {
					currentMetric = item.DeepCopy()
				} else {
					break
				}
			}

			if currentMetric == nil {
				continue
			}

			for _, container := range currentMetric.Containers {
				for resourceName, metric := range container.Usage {
					var source, method string
					containerNameGroup := strings.Split(container.Name, "-")
					if len(containerNameGroup) == 2 {
						source = containerNameGroup[0]
						method = containerNameGroup[1]
					}

					_ = p.metricsEmitter.StoreInt64(metricsNameResourcePortraitMetric, metric.MilliValue(), katalystmetrics.MetricTypeNameRaw,
						katalystmetrics.MetricTag{Key: "namespace", Val: spd.Namespace},
						katalystmetrics.MetricTag{Key: "target", Val: spd.Spec.TargetRef.Name},
						katalystmetrics.MetricTag{Key: "type", Val: spd.Spec.TargetRef.Kind},
						katalystmetrics.MetricTag{Key: "source", Val: source},
						katalystmetrics.MetricTag{Key: "method", Val: method},
						katalystmetrics.MetricTag{Key: "metric", Val: string(resourceName)})
				}
			}
		}
	}
}

func (p *ResourcePortraitIndicatorPlugin) addSPD(obj interface{}) {
	spd, ok := obj.(*apiworkload.ServiceProfileDescriptor)
	if !ok {
		klog.Errorf("[spd-resource-portrait] cannot convert obj to *apiworkload.ServiceProfileDescriptor")
		return
	}
	p.enqueueSPD(spd)
}

func (p *ResourcePortraitIndicatorPlugin) updateSPD(_, newObj interface{}) {
	spd, ok := newObj.(*apiworkload.ServiceProfileDescriptor)
	if !ok {
		klog.Errorf("[spd-resource-portrait] cannot convert obj to *apiworkload.ServiceProfileDescriptor")
		return
	}
	p.enqueueSPD(spd)
}

func (p *ResourcePortraitIndicatorPlugin) enqueueSPD(spd *apiworkload.ServiceProfileDescriptor) {
	if spd == nil {
		klog.Warning("[spd-resource-portrait] trying to enqueue a nil spd")
		return
	}

	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(spd)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("[spd-resource-portrait] couldn't get key for workload %#v: %v", spd, err))
		return
	}

	p.spdQueue.Add(key)
}

func (p *ResourcePortraitIndicatorPlugin) spdWorker() {
	for p.processNextSPD() {
	}
}

func (p *ResourcePortraitIndicatorPlugin) processNextSPD() bool {
	key, quit := p.spdQueue.Get()
	if quit {
		return false
	}
	defer p.spdQueue.Done(key)

	err := p.syncSPD(key.(string))
	if err == nil {
		p.spdQueue.Forget(key)
		return true
	}

	utilruntime.HandleError(fmt.Errorf("sync %q failed with %v", key, err))
	p.spdQueue.AddRateLimited(key)

	return true
}

func (p *ResourcePortraitIndicatorPlugin) syncSPD(key string) error {
	klog.V(5).Infof("[spd-resource-portrait] syncing spd [%v]", key)
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("[spd-resource-portrait] failed to split namespace and name from spd key %s", key)
		return err
	}

	spd, err := p.spdLister.ServiceProfileDescriptors(namespace).Get(name)
	if err != nil {
		klog.Errorf("[spd-resource-portrait] failed to get spd [%v]", key)
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	// fetch resource portrait config from spd
	rpIndicator := &apiconfig.ResourcePortraitIndicators{}
	_, err = util.GetSPDExtendedIndicators(spd, rpIndicator)
	if err != nil {
		klog.Errorf("[spd-resource-portrait] failed to get resource portrait configs from spd [%v] error: %v", key, err)
		return err
	} else if rpIndicator == nil || len(rpIndicator.Configs) == 0 {
		return nil
	}

	// refresh portrait and write into spd
	updated, aggMetrics := p.refreshPortrait(spd, rpIndicator)
	if updated {
		p.updater.UpdateAggMetrics(types.NamespacedName{
			Namespace: spd.Namespace,
			Name:      spd.Name,
		}, []apiworkload.AggPodMetrics{*aggMetrics})
	}

	return nil
}

func (p *ResourcePortraitIndicatorPlugin) refreshPortrait(spd *apiworkload.ServiceProfileDescriptor, rpIndicator *apiconfig.ResourcePortraitIndicators) (updated bool, aggMetrics *apiworkload.AggPodMetrics) {
	// filter resource portrait configurations that do not meet requirements
	rpIndicator = filterResourcePortraitIndicators(rpIndicator)
	if rpIndicator == nil || len(rpIndicator.Configs) == 0 {
		return false, nil
	}
	klog.V(5).Infof("[spd-resource-portrait] refreshing spd %s/%s", spd.Namespace, spd.Name)

	// get the last refresh time of the portrait
	aggMetrics = getAggMetricsFromSPD(spd)
	metricsRefreshRecord := generateMetricsRefreshRecord(aggMetrics)
	for _, algoConf := range rpIndicator.Configs {
		if firstTimestamp, ok := metricsRefreshRecord[fmt.Sprintf("%s-%s", algoConf.Source, algoConf.AlgorithmConfig.Method)]; ok {
			if firstTimestamp.Add(algoConf.AlgorithmConfig.ResyncPeriod * time.Second).After(time.Now()) {
				continue
			}
		}

		if algoConf.AlgorithmConfig.Params == nil {
			algoConf.AlgorithmConfig.Params = map[string]string{}
		}
		algoConf.AlgorithmConfig.Params[defaultPredictionInputKeySteps] = strconv.Itoa(algoConf.AlgorithmConfig.TimeWindow.PredictionSteps)

		originMetrics := p.dataSourceClient.Query(spd.Spec.TargetRef.Name, spd.Namespace, &algoConf)
		algorithmProvider, err := NewAlgorithmProvider(p.conf.AlgorithmServingAddress, algoConf.AlgorithmConfig.Method)
		if err != nil {
			klog.Warningf("[spd-resource-portrait] create algorithm provider failed: %v", err)
			continue
		}

		algorithmProvider.SetConfig(algoConf.AlgorithmConfig.Params)
		algorithmProvider.SetMetrics(originMetrics)
		timeseries, groupData, err := algorithmProvider.Call()
		if err != nil {
			klog.Warningf("[spd-resource-portrait]failed to call algorithm provider: %v", err)
			continue
		}

		aggMetrics = convertAlgorithmResultToAggMetrics(aggMetrics, &algoConf, timeseries, groupData)
		updated = true
	}

	aggMetrics.Scope = ResourcePortraitPluginName
	return
}

func ResourcePortraitIndicatorPluginInitFunc(ctx context.Context, conf *controller.SPDConfig, _ interface{},
	spdWorkloadInformer map[schema.GroupVersionResource]native.DynamicInformer,
	controlCtx *katalystbase.GenericContext, updater indicatorplugin.IndicatorUpdater,
) (indicatorplugin.IndicatorPlugin, error) {
	p := &ResourcePortraitIndicatorPlugin{
		ctx:            ctx,
		conf:           conf.ResourcePortraitIndicatorPluginConfig,
		spdQueue:       workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "spd"),
		updater:        updater,
		metricsEmitter: controlCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(ResourcePortraitExtendedSpecName),
	}
	p.conf.ReSyncPeriod = conf.ReSyncPeriod

	p.configManager = newResourcePortraitConfigManager(types.NamespacedName{Namespace: conf.AlgorithmConfigMapNamespace, Name: conf.AlgorithmConfigMapName}, controlCtx.Client.KubeClient.CoreV1(), p.conf.ReSyncPeriod)
	p.spdLister = controlCtx.InternalInformerFactory.Workload().V1alpha1().ServiceProfileDescriptors().Lister()
	p.workloadLister = make(map[schema.GroupVersionResource]cache.GenericLister, len(spdWorkloadInformer))
	for gvr, wf := range spdWorkloadInformer {
		p.workloadLister[gvr] = wf.Informer.Lister()
	}

	spdInformer := controlCtx.InternalInformerFactory.Workload().V1alpha1().ServiceProfileDescriptors()
	spdInformer.Informer().AddEventHandlerWithResyncPeriod(cache.ResourceEventHandlerFuncs{
		AddFunc:    p.addSPD,
		UpdateFunc: p.updateSPD,
	}, conf.ReSyncPeriod)

	dataSourceClient, err := NewDataSourceClient(conf.DataSource, &conf.DataSourcePromConfig)
	if err != nil {
		return nil, err
	}
	p.dataSourceClient = dataSourceClient

	return p, nil
}
