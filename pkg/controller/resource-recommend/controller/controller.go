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

package controller

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	"github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/datasource"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/datasource/prometheus"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/oom"
	processormanager "github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/processor/manager"
	recommendermanager "github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/recommender/manager"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")

	preCacheObjects = []client.Object{
		&v1alpha1.ResourceRecommend{},
	}
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(v1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

type ResourceRecommender struct {
	ctx         context.Context
	genericConf *generic.GenericConfiguration
	opts        *controller.ResourceRecommenderConfig
}

func NewResourceRecommenderController(ctx context.Context, genericConf *generic.GenericConfiguration, opts *controller.ResourceRecommenderConfig) (*ResourceRecommender, error) {
	return &ResourceRecommender{
		ctx:         ctx,
		genericConf: genericConf,
		opts:        opts,
	}, nil
}

func (r ResourceRecommender) Run() {
	config := ctrl.GetConfigOrDie()
	config.QPS = r.genericConf.ClientConnection.QPS
	config.Burst = int(r.genericConf.ClientConnection.Burst)

	ctrlOptions := ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     ":" + r.opts.MetricsBindPort,
		HealthProbeBindAddress: ":" + r.opts.HealthProbeBindPort,
		Port:                   9443,
	}

	mgr, err := ctrl.NewManager(config, ctrlOptions)
	if err != nil {
		klog.Fatal(fmt.Sprintf("unable to start manager: %s", err))
		os.Exit(1)
	}

	if err = mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		klog.Fatal(fmt.Sprintf("unable to set up health check: %s", err))
		os.Exit(1)
	}
	if err = mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		klog.Fatal(fmt.Sprintf("unable to set up ready check: %s", err))
		os.Exit(1)
	}

	dataProxy := initDataSources(r.opts)
	klog.Infof("successfully init data proxy %v", *dataProxy)

	// Processor Manager
	processorManager := processormanager.NewManager(dataProxy, mgr.GetClient())
	go func() {
		defer func() {
			if r := recover(); r != nil {
				err = errors.Errorf("start processor panic: %v", r.(error))
				klog.Error(err)
				panic(err)
			}
		}()
		processorManager.StartProcess(r.ctx)
	}()

	// OOM Recorder
	podOOMRecorder := &PodOOMRecorderController{
		PodOOMRecorder: &oom.PodOOMRecorder{
			Client:             mgr.GetClient(),
			OOMRecordMaxNumber: r.opts.OOMRecordMaxNumber,
		},
	}
	if err = podOOMRecorder.SetupWithManager(mgr); err != nil {
		klog.Fatal(fmt.Sprintf("Unable to create controller: %s", err))
		os.Exit(1)
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				err = errors.Errorf("Run oom recorder panic: %v", r.(error))
				klog.Error(err)
				panic(err)
			}
		}()
		for count := 0; count < 100; count++ {
			cacheReady := mgr.GetCache().WaitForCacheSync(r.ctx)
			if cacheReady {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err = podOOMRecorder.Run(r.ctx.Done()); err != nil {
			klog.Warningf("Run oom recorder failed: %v", err)
		}
	}()

	recommenderManager := recommendermanager.NewManager(*processorManager, podOOMRecorder)

	for _, obj := range preCacheObjects {
		_, _ = mgr.GetCache().GetInformer(context.TODO(), obj)
	}

	// Resource Recommend Controller
	resourceRecommendController := &ResourceRecommendController{
		Client:             mgr.GetClient(),
		Scheme:             mgr.GetScheme(),
		ProcessorManager:   processorManager,
		RecommenderManager: recommenderManager,
	}

	if err = resourceRecommendController.SetupWithManager(mgr); err != nil {
		klog.Fatal(fmt.Sprintf("ResourceRecommend Controller unable to SetupWithManager, err: %s", err))
		os.Exit(1)
	}

	//+kubebuilder:scaffold:builder

	setupLog.Info("starting manager")
	if err = mgr.Start(r.ctx); err != nil {
		klog.Fatal(fmt.Sprintf("problem running manager: %s", err))
		os.Exit(1)
	}
}

func initDataSources(opts *controller.ResourceRecommenderConfig) *datasource.Proxy {
	dataProxy := datasource.NewProxy()
	for _, datasourceProvider := range opts.DataSource {
		switch datasourceProvider {
		case string(datasource.PrometheusDatasource):
			fallthrough
		default:
			// default is prom
			prometheusProvider, err := prometheus.NewPrometheus(&opts.DataSourcePromConfig)
			if err != nil {
				klog.Exitf("unable to create datasource provider %v, err: %v", prometheusProvider, err)
				panic(err)
			}
			dataProxy.RegisterDatasource(datasource.PrometheusDatasource, prometheusProvider)
		}
	}
	return dataProxy
}
