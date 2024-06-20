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

// Package client is the package that generate K8S kubeConfig and clientSet; and
// any new CRD and its corresponding clientSet should be added here.
// besides, this package is the only package that update/patch actions should happen.
package client // import "github.com/kubewharf/katalyst-core/pkg/client"

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	componentbaseconfig "k8s.io/component-base/config"
	aggregator "k8s.io/kube-aggregator/pkg/client/clientset_generated/clientset"
	"k8s.io/metrics/pkg/client/clientset/versioned"
	"k8s.io/metrics/pkg/client/custom_metrics"
	customclient "k8s.io/metrics/pkg/client/custom_metrics"
	cmfake "k8s.io/metrics/pkg/client/custom_metrics/fake"
	"k8s.io/metrics/pkg/client/external_metrics"
	externalclient "k8s.io/metrics/pkg/client/external_metrics"
	emfake "k8s.io/metrics/pkg/client/external_metrics/fake"
	"sigs.k8s.io/custom-metrics-apiserver/pkg/dynamicmapper"

	clientset "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned"
)

// GenericClientSet defines a generic client contains clients that are needed
type GenericClientSet struct {
	cfg *rest.Config

	MetaClient       metadata.Interface
	KubeClient       kubernetes.Interface
	InternalClient   clientset.Interface
	DynamicClient    dynamic.Interface
	DiscoveryClient  discovery.DiscoveryInterface
	AggregatorClient aggregator.Interface
	MetricClient     versioned.Interface

	CustomClient   customclient.CustomMetricsClient
	ExternalClient externalclient.ExternalMetricsClient
}

// BuildMetricClient builds kubernetes native metrics-clients; and metrics-clients
// can't be build in init process, since discovery mapper will be initialized at the
// same time, which is usually not needed for agents (to avoid too many connections).
func (g *GenericClientSet) BuildMetricClient(mapper *dynamicmapper.RegeneratingDiscoveryRESTMapper) {
	apiVersionsGetter := custom_metrics.NewAvailableAPIsGetter(g.KubeClient.Discovery())
	g.CustomClient = custom_metrics.NewForConfig(g.cfg, mapper, apiVersionsGetter)
	g.ExternalClient = external_metrics.NewForConfigOrDie(g.cfg)
	g.AggregatorClient = aggregator.NewForConfigOrDie(g.cfg)
}

// newForConfig creates a new clientSet for the given config.
func newForConfig(cfg *rest.Config) (*GenericClientSet, error) {
	cWithProtobuf := rest.CopyConfig(cfg)
	cWithProtobuf.ContentType = runtime.ContentTypeProtobuf

	metaClient, err := metadata.NewForConfig(cWithProtobuf)
	if err != nil {
		return nil, err
	}

	kubeClient, err := kubernetes.NewForConfig(cWithProtobuf)
	if err != nil {
		return nil, err
	}

	internalClient, err := clientset.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}

	dynamicClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, err
	}

	metricClient, err := versioned.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}

	return &GenericClientSet{
		cfg:             cfg,
		MetaClient:      metaClient,
		KubeClient:      kubeClient,
		InternalClient:  internalClient,
		DynamicClient:   dynamicClient,
		DiscoveryClient: discoveryClient,
		MetricClient:    metricClient,

		CustomClient:   &cmfake.FakeCustomMetricsClient{},
		ExternalClient: &emfake.FakeExternalMetricsClient{},
	}, nil
}

// newForConfigOrDie creates a new clientSet for the given config.
func newForConfigOrDie(cfg *rest.Config) *GenericClientSet {
	gc, err := newForConfig(cfg)
	if err != nil {
		panic(err)
	}
	return gc
}

// BuildGenericClient returns KubeConfig for given master and KubeConfig raw string
func BuildGenericClient(config componentbaseconfig.ClientConnectionConfiguration, masterURL, kubeConfig, name string) (*GenericClientSet, error) {
	inputMasterURL := masterURL

	// if kube-config is empty, use in cluster configuration
	if kubeConfig == "" {
		masterURL = ""
	}

	cfg, err := clientcmd.BuildConfigFromFlags(masterURL, kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("BuildConfigFromFlags err:%v", err)
	}

	if inputMasterURL != "" {
		cfg.Host = inputMasterURL
	}

	// reset configurations according to client-connection configs
	cfg.DisableCompression = true
	cfg.AcceptContentTypes = config.AcceptContentTypes
	cfg.ContentType = config.ContentType
	cfg.QPS = config.QPS
	cfg.Burst = int(config.Burst)
	cfg.UserAgent = fmt.Sprintf("%s/%s", cfg.UserAgent, name)

	return newForConfigOrDie(cfg), nil
}
