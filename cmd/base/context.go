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

package katalyst_base

import (
	"context"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/metadata/metadatainformer"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"
	aggregator "k8s.io/kube-aggregator/pkg/client/informers/externalversions"
	"sigs.k8s.io/custom-metrics-apiserver/pkg/dynamicmapper"

	"github.com/kubewharf/katalyst-api/pkg/client/informers/externalversions"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/credential"
	"github.com/kubewharf/katalyst-core/pkg/util/credential/authorization"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const (
	healthZPath = "/healthz"
	debugPrefix = "/debug"
)

// GenericOptions is used as an extendable way to support
type GenericOptions func(i interface{})

type GenericContext struct {
	*http.Server
	httpHandler   *process.HTTPHandler
	healthChecker *HealthzChecker

	// those following components are shared by all generic components.
	//nolint
	BroadcastAdapter events.EventBroadcasterAdapter
	Client           *client.GenericClientSet
	EmitterPool      metricspool.MetricsEmitterPool

	// those following informer factories give access to informers for the component.
	// actually, for agent, we should be cautious if we decide to start informers (
	// to reduce connections with APIServer).
	//
	// since those variables may be un-initialized in some component, we must be
	// very careful when we use them
	MetaInformerFactory       metadatainformer.SharedInformerFactory
	KubeInformerFactory       informers.SharedInformerFactory
	InternalInformerFactory   externalversions.SharedInformerFactory
	DynamicInformerFactory    dynamicinformer.DynamicSharedInformerFactory
	AggregatorInformerFactory aggregator.SharedInformerFactory
	DynamicResourcesManager   *native.DynamicResourcesManager

	// if we want to support for transformed informer for a certain object,
	// it must be enabled transparently
	transformedInformerForPod bool

	// DisabledByDefault is the set of components which is disabled by default
	DisabledByDefault sets.String
}

func NewGenericContext(
	clientSet *client.GenericClientSet,
	labelSelector string,
	dynamicResources []string,
	disabledByDefault sets.String,
	genericConf *generic.GenericConfiguration,
	component consts.KatalystComponent,
	dynamicConfiguration *dynamic.DynamicAgentConfiguration,
) (*GenericContext, error) {
	var (
		err                       error
		metaInformerFactory       metadatainformer.SharedInformerFactory
		kubeInformerFactory       informers.SharedInformerFactory
		internalInformerFactory   externalversions.SharedInformerFactory
		dynamicInformerFactory    dynamicinformer.DynamicSharedInformerFactory
		aggregatorInformerFactory aggregator.SharedInformerFactory
		mapper                    *dynamicmapper.RegeneratingDiscoveryRESTMapper
		dynamicResourceManager    *native.DynamicResourcesManager
	)

	// agent no need initialize informer
	if component != consts.KatalystComponentAgent {
		metaInformerFactory = metadatainformer.NewFilteredSharedInformerFactory(clientSet.MetaClient, time.Hour*24, "",
			func(options *metav1.ListOptions) {
				options.LabelSelector = labelSelector
			})

		kubeInformerFactory = informers.NewSharedInformerFactoryWithOptions(clientSet.KubeClient, time.Hour*24,
			informers.WithTweakListOptions(func(options *metav1.ListOptions) {
				options.LabelSelector = labelSelector
			}))

		internalInformerFactory = externalversions.NewSharedInformerFactoryWithOptions(clientSet.InternalClient, time.Hour*24,
			externalversions.WithTweakListOptions(func(options *metav1.ListOptions) {
				options.LabelSelector = labelSelector
			}))

		dynamicInformerFactory = dynamicinformer.NewFilteredDynamicSharedInformerFactory(clientSet.DynamicClient, time.Hour*24,
			metav1.NamespaceAll, func(options *metav1.ListOptions) {
				options.LabelSelector = labelSelector
			})

		mapper, err = dynamicmapper.NewRESTMapper(clientSet.DiscoveryClient, time.Minute)
		if err != nil {
			return nil, err
		}

		dynamicResourceManager, err = native.NewDynamicResourcesManager(dynamicResources, mapper, dynamicInformerFactory)
		if err != nil {
			return nil, err
		}

		if component == consts.KatalystComponentMetric {
			clientSet.BuildMetricClient(mapper)
			aggregatorInformerFactory = aggregator.NewSharedInformerFactory(clientSet.AggregatorClient, time.Hour*24)
		}
	}

	mux := http.NewServeMux()
	emitterPool, err := metricspool.NewOpenTelemetryPrometheusMetricsEmitterPool(genericConf.MetricsConfiguration, mux)
	if err != nil {
		return nil, err
	}

	customMetricsEmitterPool := metricspool.NewCustomMetricsEmitterPool(emitterPool)

	// CreateEventRecorder create a v1 event (k8s 1.19 or later supported) recorder,
	// which uses discovery client to check whether api server support v1 event, if not,
	// it will use corev1 event recorder and wrap it with a v1 event recorder adapter.
	broadcastAdapter := events.NewEventBroadcasterAdapter(clientSet.KubeClient)

	httpHandler := process.NewHTTPHandler(genericConf.GenericEndpointHandleChains, []string{healthZPath, debugPrefix},
		genericConf.HttpStrictAuthentication, customMetricsEmitterPool.GetDefaultMetricsEmitter())

	// since some authentication implementation needs kcc and kcc only support agent component, so we only enable
	// authentication for agent component for now.
	if component == consts.KatalystComponentAgent {
		cred, credErr := credential.GetCredential(genericConf, dynamicConfiguration)
		if credErr != nil {
			return nil, credErr
		}
		err = httpHandler.WithCredential(cred)
		if err != nil {
			return nil, err
		}

		accessControl, acErr := authorization.GetAccessControl(genericConf, dynamicConfiguration)
		if acErr != nil {
			return nil, acErr
		}
		err = httpHandler.WithAuthorization(accessControl)
		if err != nil {
			return nil, err
		}
	}

	c := &GenericContext{
		httpHandler: httpHandler,
		Server: &http.Server{
			Handler: httpHandler.WithHandleChain(mux),
			Addr:    genericConf.GenericEndpoint,
		},
		healthChecker:             NewHealthzChecker(customMetricsEmitterPool.GetDefaultMetricsEmitter()),
		DisabledByDefault:         disabledByDefault,
		MetaInformerFactory:       metaInformerFactory,
		KubeInformerFactory:       kubeInformerFactory,
		InternalInformerFactory:   internalInformerFactory,
		DynamicInformerFactory:    dynamicInformerFactory,
		AggregatorInformerFactory: aggregatorInformerFactory,
		BroadcastAdapter:          broadcastAdapter,
		Client:                    clientSet,
		EmitterPool:               customMetricsEmitterPool,
		DynamicResourcesManager:   dynamicResourceManager,
		transformedInformerForPod: genericConf.TransformedInformerForPod,
	}

	// add profiling and health check http paths listening on generic endpoint
	serveProfilingHTTP(mux)
	c.serveHealthZHTTP(mux, genericConf.EnableHealthzCheck)

	return c, nil
}

// IsEnabled checks if the context's components enabled or not
func (c *GenericContext) IsEnabled(name string, components []string) bool {
	return general.IsNameEnabled(name, c.DisabledByDefault, components)
}

// SetDefaultMetricsEmitter to set default metrics emitter by custom metric emitter
func (c *GenericContext) SetDefaultMetricsEmitter(metricEmitter metrics.MetricEmitter) {
	c.EmitterPool.SetDefaultMetricsEmitter(metricEmitter)
}

// Run starts the generic components
func (c *GenericContext) Run(ctx context.Context) {
	c.httpHandler.Run(ctx)
	c.healthChecker.Run(ctx)
	c.EmitterPool.Run(ctx)
	c.BroadcastAdapter.StartRecordingToSink(ctx.Done())
	go func() {
		klog.Fatal(c.ListenAndServe())
		<-ctx.Done()
	}()
}

// StartInformer starts the shared informer factories;
// informer is reentrant, so it's no need to check if context has been started
func (c *GenericContext) StartInformer(ctx context.Context) {
	if c.MetaInformerFactory != nil {
		c.MetaInformerFactory.Start(ctx.Done())
	}

	if c.KubeInformerFactory != nil {
		if transformers, ok := native.GetPodTransformer(); ok && c.transformedInformerForPod {
			_ = c.KubeInformerFactory.Core().V1().Pods().Informer().SetTransform(transformers)
		}
		c.KubeInformerFactory.Start(ctx.Done())
	}

	if c.InternalInformerFactory != nil {
		c.InternalInformerFactory.Start(ctx.Done())
	}

	if c.DynamicInformerFactory != nil {
		c.DynamicInformerFactory.Start(ctx.Done())
	}

	if c.DynamicResourcesManager != nil {
		c.DynamicResourcesManager.Run(ctx)
	}

	if c.AggregatorInformerFactory != nil {
		c.AggregatorInformerFactory.Start(ctx.Done())
	}
}

// serveHealthZHTTP is used to provide health check for current running components.
func (c *GenericContext) serveHealthZHTTP(mux *http.ServeMux, enableHealthzCheck bool) {
	mux.HandleFunc(healthZPath, func(w http.ResponseWriter, r *http.Request) {
		ok, content := c.healthChecker.CheckHealthy()
		if ok || !enableHealthzCheck {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(content))
		} else {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(content))
		}
	})
}

// serveProfilingHTTP is used to provide pprof metrics for current running components.
func serveProfilingHTTP(mux *http.ServeMux) {
	mux.Handle("/debug/pprof/", http.HandlerFunc(pprof.Index))
	mux.Handle("/debug/pprof/cmdline", http.HandlerFunc(pprof.Cmdline))
	mux.Handle("/debug/pprof/profile", http.HandlerFunc(pprof.Profile))
	mux.Handle("/debug/pprof/symbol", http.HandlerFunc(pprof.Symbol))
	mux.Handle("/debug/pprof/trace", http.HandlerFunc(pprof.Trace))

	mux.Handle("/debug/metrics", promhttp.Handler())
}
