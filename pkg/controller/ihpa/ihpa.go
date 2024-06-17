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
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	v2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	v1 "k8s.io/client-go/kubernetes/typed/apps/v1"
	hpalister "k8s.io/client-go/listers/autoscaling/v2"
	"k8s.io/client-go/restmapper"
	scaleclient "k8s.io/client-go/scale"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"

	apiautoscaling "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha2"
	apiworkload "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	apiautoscalingclient "github.com/kubewharf/katalyst-api/pkg/client/listers/autoscaling/v1alpha2"
	apiWorkloadListers "github.com/kubewharf/katalyst-api/pkg/client/listers/workload/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-api/pkg/metric"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	resourceportrait "github.com/kubewharf/katalyst-core/pkg/controller/spd/indicator-plugin/plugins/resource-portrait"
	katalystmetrics "github.com/kubewharf/katalyst-core/pkg/metrics"
)

const (
	IHPAControllerName            = "ihpa"
	metricsNameIHPAReplicasMetric = "ihpa_replicas_metrics"
	metricsNameIHPACurrentMetric  = "ihpa_current_metrics"
)

var ResourcePortraitContainerName = resourceportrait.GenerateFakeContainerName(IHPAControllerName, resourceportrait.ResourcePortraitMethodPredict)

var deploymentGVK = schema.GroupVersionKind{
	Group:   "apps",
	Version: "v1",
	Kind:    "Deployment",
}

var statefulSetGVK = schema.GroupVersionKind{
	Group:   "apps",
	Version: "v1",
	Kind:    "StatefulSet",
}

type IHPAController struct {
	ctx  context.Context
	conf *controller.IHPAConfig

	scaler     scaleclient.ScalesGetter
	restMapper meta.RESTMapper

	hpaManager  control.HPAManager
	vwManager   control.VirtualWorkloadManager
	ihpaUpdater control.IHPAUpdater
	spdUpdater  *control.SPDControlImp

	syncedFunc    []cache.InformerSynced
	ihpaSyncQueue workqueue.RateLimitingInterface

	ihpaLister            apiautoscalingclient.IntelligentHorizontalPodAutoscalerLister
	virtualWorkloadLister apiautoscalingclient.VirtualWorkloadLister
	spdLister             apiWorkloadListers.ServiceProfileDescriptorLister
	hpaLister             hpalister.HorizontalPodAutoscalerLister

	appsClient     v1.AppsV1Interface
	workloadLister map[schema.GroupVersionKind]cache.GenericLister

	metricsEmitter katalystmetrics.MetricEmitter
}

func (p *IHPAController) Run() {
	defer utilruntime.HandleCrash()
	defer p.ihpaSyncQueue.ShutDown()
	defer klog.Infof("shutting down %s controller", IHPAControllerName)

	if !cache.WaitForCacheSync(p.ctx.Done(), p.syncedFunc...) {
		utilruntime.HandleError(fmt.Errorf("unable to sync caches for %s controller", IHPAControllerName))
		return
	}
	klog.Infof("caches are synced for %s controller", IHPAControllerName)

	go wait.Until(p.emitMetrics, p.conf.ResyncPeriod, p.ctx.Done())

	for i := 0; i < p.conf.SyncWorkers; i++ {
		go wait.Until(p.ihpaWorker, p.conf.ResyncPeriod, p.ctx.Done())
	}

	<-p.ctx.Done()
}

func (p *IHPAController) emitMetrics() {
	ihpaList, err := p.ihpaLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("[ihpa] failed to list all ihpa: %v", err)
		return
	}

	for _, ihpa := range ihpaList {
		p.emitMetric(int64(ihpa.Status.DesiredReplicas), metricsNameIHPAReplicasMetric, ihpa.Namespace, ihpa.Name, "desired")
		p.emitMetric(int64(ihpa.Status.CurrentReplicas), metricsNameIHPAReplicasMetric, ihpa.Namespace, ihpa.Name, "current")
		for _, currentMetric := range ihpa.Status.CurrentMetrics {
			switch currentMetric.Type {
			case v2.ResourceMetricSourceType:
				if currentMetric.Resource == nil {
					continue
				}
				p.emitMetric(int64(pointer.Int32Deref(currentMetric.Resource.Current.AverageUtilization, 0)), metricsNameIHPACurrentMetric, ihpa.Namespace, ihpa.Name, string(currentMetric.Resource.Name))
			case v2.ExternalMetricSourceType:
				if currentMetric.External == nil || currentMetric.External.Current.AverageValue == nil || currentMetric.External.Metric.Selector == nil ||
					currentMetric.External.Metric.Selector.MatchLabels[metric.MetricSelectorKeySPDResourceName] == "" {
					continue
				}
				p.emitMetric(currentMetric.External.Current.AverageValue.MilliValue(), metricsNameIHPACurrentMetric, ihpa.Namespace, ihpa.Name, currentMetric.External.Metric.Selector.MatchLabels[metric.MetricSelectorKeySPDResourceName])
			}
		}
	}
}

func (p *IHPAController) emitMetric(value int64, metricName, namespace, name, valueType string) {
	_ = p.metricsEmitter.StoreInt64(metricName, value, katalystmetrics.MetricTypeNameRaw,
		katalystmetrics.MetricTag{Key: "namespace", Val: namespace},
		katalystmetrics.MetricTag{Key: "name", Val: name},
		katalystmetrics.MetricTag{Key: "type", Val: valueType},
	)
}

func (p *IHPAController) addIHPA(obj interface{}) {
	ihpa, ok := obj.(*apiautoscaling.IntelligentHorizontalPodAutoscaler)
	if !ok {
		klog.Errorf("[ihpa] cannot convert obj to *IntelligentHorizontalPodAutoscaler")
		return
	}
	p.enqueueIHPA(ihpa)
}

func (p *IHPAController) updateIHPA(_, newObj interface{}) {
	ihpa, ok := newObj.(*apiautoscaling.IntelligentHorizontalPodAutoscaler)
	if !ok {
		klog.Errorf("[ihpa] cannot convert obj to *IntelligentHorizontalPodAutoscale")
		return
	}
	p.enqueueIHPA(ihpa)
}

func (p *IHPAController) enqueueIHPA(ihpa *apiautoscaling.IntelligentHorizontalPodAutoscaler) {
	if ihpa == nil {
		klog.Warning("[ihpa] trying to enqueue a nil ihpa")
		return
	}

	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(ihpa)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("[ihpa] couldn't get key for ihpa %#v: %v", ihpa, err))
		return
	}

	p.ihpaSyncQueue.Add(key)
}

func (p *IHPAController) ihpaWorker() {
	for p.processNextIHPA() {
	}
}

func (p *IHPAController) processNextIHPA() bool {
	key, quit := p.ihpaSyncQueue.Get()
	if quit {
		return false
	}
	defer p.ihpaSyncQueue.Done(key)

	err := p.syncIHPA(key.(string))
	if err == nil {
		p.ihpaSyncQueue.Forget(key)
		return true
	}

	utilruntime.HandleError(fmt.Errorf("sync %q failed with %v", key, err))
	p.ihpaSyncQueue.AddRateLimited(key)

	return true
}

func (p *IHPAController) syncIHPA(key string) error {
	klog.V(5).Infof("[ihpa] syncing ihpa [%v]", key)
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("[ihpa] failed to split namespace and name from ihpa key %s", key)
		return err
	}

	ihpa, err := p.ihpaLister.IntelligentHorizontalPodAutoscalers(namespace).Get(name)
	if err != nil {
		klog.Errorf("[ihpa] failed to get ihpa [%v]", key)
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	ihpaCopy := ihpa.DeepCopy()

	podTemplate, err := p.syncWorkload(ihpa)
	if err != nil {
		klog.Errorf("[ihpa] failed to sync workload for ihpa [%v]", key)
		return err
	}

	hpa, err := p.syncHPA(ihpa, podTemplate)
	if err != nil {
		klog.Errorf("[ihpa] failed to sync hpa ihpa [%v]", key)
		return err
	}

	spd, err := p.syncSPD(ihpa)
	if err != nil {
		klog.Errorf("[ihpa] failed to sync spd for ihpa [%v]", key)
		return err
	}

	err = p.syncVirtualWorkload(ihpa)
	if err != nil {
		klog.Errorf("[ihpa] failed to sync virtual workload for ihpa [%v]", key)
		return err
	}

	updateStatus(ihpa, hpa, spd)
	if !apiequality.Semantic.DeepEqual(ihpa.Status, ihpaCopy.Status) {
		_, err = p.ihpaUpdater.UpdateStatus(p.ctx, ihpa, metav1.UpdateOptions{})
		if err != nil {
			klog.Errorf("[ihpa] failed to update ihpa [%v]", key)
			return err
		}
	}

	return nil
}

func (p *IHPAController) syncWorkload(ihpa *apiautoscaling.IntelligentHorizontalPodAutoscaler) (*corev1.PodTemplateSpec, error) {
	gvk := schema.FromAPIVersionAndKind(ihpa.Spec.Autoscaler.ScaleTargetRef.APIVersion, ihpa.Spec.Autoscaler.ScaleTargetRef.Kind)
	if lister, ok := p.workloadLister[gvk]; ok {
		workload, err := lister.ByNamespace(ihpa.Namespace).Get(ihpa.Spec.Autoscaler.ScaleTargetRef.Name)
		if err != nil {
			return nil, err
		}

		unstructured, ok := workload.(runtime.Unstructured)
		if !ok {
			return nil, fmt.Errorf("[ihpa] failed to convert workload to Unstructured")
		}

		switch gvk {
		case deploymentGVK:
			deployment := &appsv1.Deployment{}
			err = runtime.DefaultUnstructuredConverter.FromUnstructured(unstructured.UnstructuredContent(), deployment)
			if err != nil {
				return nil, err
			}

			if deployment.Annotations[consts.WorkloadAnnotationSPDEnableKey] == consts.WorkloadAnnotationSPDEnabled {
				return &deployment.Spec.Template, nil
			} else if deployment.Annotations == nil {
				deployment.Annotations = map[string]string{}
			}

			deployment.Annotations[consts.WorkloadAnnotationSPDEnableKey] = consts.WorkloadAnnotationSPDEnabled
			deployment, err = p.appsClient.Deployments(deployment.Namespace).Update(p.ctx, deployment, metav1.UpdateOptions{})
			if err != nil {
				return nil, err
			}
			return &deployment.Spec.Template, nil
		case statefulSetGVK:
			statefulSet := &appsv1.StatefulSet{}
			err = runtime.DefaultUnstructuredConverter.FromUnstructured(unstructured.UnstructuredContent(), statefulSet)
			if err != nil {
				return nil, err
			}

			if statefulSet.Annotations[consts.WorkloadAnnotationSPDEnableKey] == consts.WorkloadAnnotationSPDEnabled {
				return &statefulSet.Spec.Template, nil
			} else if statefulSet.Annotations == nil {
				statefulSet.Annotations = map[string]string{}
			}

			statefulSet.Annotations[consts.WorkloadAnnotationSPDEnableKey] = consts.WorkloadAnnotationSPDEnabled
			statefulSet, err = p.appsClient.StatefulSets(statefulSet.Namespace).Update(p.ctx, statefulSet, metav1.UpdateOptions{})
			if err != nil {
				return nil, err
			}
			return &statefulSet.Spec.Template, nil
		}
	}
	return nil, fmt.Errorf("[ihpa] failed to get workload lister for gvk %v", gvk)
}

func (p *IHPAController) syncSPD(ihpa *apiautoscaling.IntelligentHorizontalPodAutoscaler) (*apiworkload.ServiceProfileDescriptor, error) {
	return p.spdLister.ServiceProfileDescriptors(ihpa.Namespace).Get(ihpa.Spec.Autoscaler.ScaleTargetRef.Name)
}

func (p *IHPAController) syncVirtualWorkload(ihpa *apiautoscaling.IntelligentHorizontalPodAutoscaler) error {
	ref := ihpa.Spec.Autoscaler.ScaleTargetRef
	gv, err := schema.ParseGroupVersion(ref.APIVersion)
	if err != nil {
		return err
	}

	gvk := schema.GroupVersionKind{
		Group:   gv.Group,
		Version: gv.Version,
		Kind:    ref.Kind,
	}

	mappings, err := p.restMapper.RESTMappings(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return err
	}

	if len(mappings) == 0 {
		return fmt.Errorf("[ihpa] unrecognized resource: %v", gvk)
	}

	scaleObj, err := p.scaler.Scales(ihpa.Namespace).Get(
		p.ctx, mappings[0].Resource.GroupResource(),
		ref.Name, metav1.GetOptions{ResourceVersion: "0"})
	if err != nil {
		return err
	}

	vw, err := p.virtualWorkloadLister.VirtualWorkloads(ihpa.Namespace).Get(ihpa.Name)
	if err != nil {
		if errors.IsNotFound(err) {
			vw = &apiautoscaling.VirtualWorkload{
				ObjectMeta: metav1.ObjectMeta{
					Name: ihpa.Name, Namespace: ihpa.Namespace,
					OwnerReferences: []metav1.OwnerReference{generateOwnerReference(ihpa)},
				},
				Spec: apiautoscaling.VirtualWorkloadSpec{Replicas: scaleObj.Status.Replicas},
				Status: apiautoscaling.VirtualWorkloadStatus{
					Replicas: scaleObj.Spec.Replicas,
					Selector: scaleObj.Status.Selector,
				},
			}
			_, err = p.vwManager.Create(p.ctx, vw, metav1.CreateOptions{})
		}
		return err
	}

	if vw.Spec.Replicas != vw.Status.Replicas || vw.Status.Selector != scaleObj.Status.Selector {
		vw.Status.Replicas = vw.Spec.Replicas
		vw.Status.Selector = scaleObj.Status.Selector
		_, err = p.vwManager.UpdateStatus(p.ctx, vw, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
	}

	if vw.Spec.Replicas != scaleObj.Status.Replicas {
		vw.Spec.Replicas = scaleObj.Status.Replicas
		_, err = p.vwManager.Update(p.ctx, vw, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
	}

	return nil
}

func (p *IHPAController) syncHPA(ihpa *apiautoscaling.IntelligentHorizontalPodAutoscaler, podTemplate *corev1.PodTemplateSpec) (hpa *v2.HorizontalPodAutoscaler, err error) {
	hpa, err = p.hpaLister.HorizontalPodAutoscalers(ihpa.Namespace).Get(generateHPAName(ihpa.Name))
	if err != nil {
		if errors.IsNotFound(err) {
			_, err = p.hpaManager.Create(p.ctx, generateHPA(ihpa, podTemplate), metav1.CreateOptions{})
		}
		return
	}

	newHPA := generateHPA(ihpa, podTemplate)
	if !apiequality.Semantic.DeepEqual(hpa.Spec, newHPA.Spec) {
		hpa, err = p.hpaManager.Patch(p.ctx, hpa, newHPA, metav1.PatchOptions{})
		return
	}
	return
}

func NewIHPAController(ctx context.Context, controlCtx *katalystbase.GenericContext,
	genericConf *generic.GenericConfiguration, _ *controller.GenericControllerConfiguration,
	conf *controller.IHPAConfig, qosConfig *generic.QoSConfiguration, extraConf interface{},
) (*IHPAController, error) {
	ihpaInformer := controlCtx.InternalInformerFactory.Autoscaling().V1alpha2().IntelligentHorizontalPodAutoscalers()
	virtualWorkloadInformer := controlCtx.InternalInformerFactory.Autoscaling().V1alpha2().VirtualWorkloads()
	spdInformer := controlCtx.InternalInformerFactory.Workload().V1alpha1().ServiceProfileDescriptors()
	genericClient := controlCtx.Client

	ihpaController := &IHPAController{
		ctx:                   ctx,
		conf:                  conf,
		hpaLister:             controlCtx.KubeInformerFactory.Autoscaling().V2().HorizontalPodAutoscalers().Lister(),
		ihpaSyncQueue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "ihpa"),
		ihpaLister:            ihpaInformer.Lister(),
		hpaManager:            control.NewHPAManager(genericClient.KubeClient),
		vwManager:             control.NewVirtualWorkloadManager(genericClient.InternalClient),
		ihpaUpdater:           control.NewIHPAUpdater(genericClient.InternalClient),
		spdUpdater:            control.NewSPDControlImp(controlCtx.Client.InternalClient),
		virtualWorkloadLister: virtualWorkloadInformer.Lister(),
		spdLister:             spdInformer.Lister(),
		workloadLister:        map[schema.GroupVersionKind]cache.GenericLister{},
		metricsEmitter:        controlCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(IHPAControllerName),
	}

	ihpaController.syncedFunc = append(ihpaController.syncedFunc, ihpaInformer.Informer().HasSynced)
	ihpaController.syncedFunc = append(ihpaController.syncedFunc, virtualWorkloadInformer.Informer().HasSynced)
	ihpaController.syncedFunc = append(ihpaController.syncedFunc, spdInformer.Informer().HasSynced)

	ihpaInformer.Informer().AddEventHandlerWithResyncPeriod(cache.ResourceEventHandlerFuncs{
		AddFunc:    ihpaController.addIHPA,
		UpdateFunc: ihpaController.updateIHPA,
	}, ihpaController.conf.ResyncPeriod)

	workloadInformers := controlCtx.DynamicResourcesManager.GetDynamicInformers()
	for _, wf := range workloadInformers {
		ihpaController.workloadLister[wf.GVK] = wf.Informer.Lister()
		ihpaController.syncedFunc = append(ihpaController.syncedFunc, wf.Informer.Informer().HasSynced)
	}

	ihpaController.appsClient = controlCtx.Client.KubeClient.AppsV1()

	apiGroupResources, err := restmapper.GetAPIGroupResources(controlCtx.Client.DiscoveryClient)
	if err != nil {
		return nil, err
	}
	ihpaController.restMapper = restmapper.NewDiscoveryRESTMapper(apiGroupResources)
	ihpaController.scaler = scaleclient.New(controlCtx.Client.DiscoveryClient.RESTClient(), ihpaController.restMapper, dynamic.LegacyAPIPathResolverFunc, scaleclient.NewDiscoveryScaleKindResolver(controlCtx.Client.DiscoveryClient))

	return ihpaController, nil
}
