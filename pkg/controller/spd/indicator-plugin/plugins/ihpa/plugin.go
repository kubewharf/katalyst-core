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

package ihpa

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	apiautoscaling "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha2"
	apiconfig "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	apiworkload "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/client/listers/autoscaling/v1alpha2"
	apiListers "github.com/kubewharf/katalyst-api/pkg/client/listers/workload/v1alpha1"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	indicatorplugin "github.com/kubewharf/katalyst-core/pkg/controller/spd/indicator-plugin"
	resourceportrait "github.com/kubewharf/katalyst-core/pkg/controller/spd/indicator-plugin/plugins/resource-portrait"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const PluginName = "IHPAPlugin"

type Plugin struct {
	ctx          context.Context
	reSyncPeriod time.Duration

	ihpaLister v1alpha2.IntelligentHorizontalPodAutoscalerLister
	spdLister  apiListers.ServiceProfileDescriptorLister

	updater indicatorplugin.IndicatorUpdater
}

func (p *Plugin) ihpaWorker() {
	ihpas, err := p.ihpaLister.List(labels.Everything())
	if err != nil {
		klog.ErrorS(err, "[ihpa plugin] failed to list ihpa")
		return
	}

	for _, ihpa := range ihpas {
		p.syncSPD(ihpa)
	}
}

func (p *Plugin) syncSPD(ihpa *apiautoscaling.IntelligentHorizontalPodAutoscaler) {
	spd, err := p.spdLister.ServiceProfileDescriptors(ihpa.Namespace).Get(ihpa.Spec.Autoscaler.ScaleTargetRef.Name)
	if err != nil {
		return
	}

	p.updater.UpdateExtendedIndicatorSpec(types.NamespacedName{
		Namespace: spd.Namespace,
		Name:      spd.Name,
	}, []apiworkload.ServiceExtendedIndicatorSpec{{
		Name:       resourceportrait.ResourcePortraitExtendedSpecName,
		Indicators: runtime.RawExtension{Object: &apiconfig.ResourcePortraitIndicators{Configs: []apiconfig.ResourcePortraitConfig{*generateResourcePortraitConfig(ihpa)}}},
	}})
}

func (p *Plugin) Run() {
	defer utilruntime.HandleCrash()
	defer klog.Infof("shutting down spd plugin: %s", p.Name())

	go wait.Until(p.ihpaWorker, p.reSyncPeriod, p.ctx.Done())

	<-p.ctx.Done()
}

func (p *Plugin) Name() string { return PluginName }
func (p *Plugin) GetSupportedBusinessIndicatorSpec() []apiworkload.ServiceBusinessIndicatorName {
	return nil
}

func (p *Plugin) GetSupportedSystemIndicatorSpec() []apiworkload.ServiceSystemIndicatorName {
	return nil
}

func (p *Plugin) GetSupportedBusinessIndicatorStatus() []apiworkload.ServiceBusinessIndicatorName {
	return nil
}

func (p *Plugin) GetSupportedExtendedIndicatorSpec() []string {
	return nil
}

func (p *Plugin) GetSupportedAggMetricsStatus() []string {
	return nil
}

func PluginInitFunc(ctx context.Context, conf *controller.SPDConfig, _ interface{},
	_ map[schema.GroupVersionResource]native.DynamicInformer,
	controlCtx *katalystbase.GenericContext, updater indicatorplugin.IndicatorUpdater,
) (indicatorplugin.IndicatorPlugin, error) {
	return &Plugin{
		ctx:          ctx,
		reSyncPeriod: conf.ReSyncPeriod,
		spdLister:    controlCtx.InternalInformerFactory.Workload().V1alpha1().ServiceProfileDescriptors().Lister(),
		ihpaLister:   controlCtx.InternalInformerFactory.Autoscaling().V1alpha2().IntelligentHorizontalPodAutoscalers().Lister(),
		updater:      updater,
	}, nil
}
