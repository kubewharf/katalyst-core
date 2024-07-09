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

package prediction

import (
	"context"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v13 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	v12 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/controller/overcommit/prediction/common"
	"github.com/kubewharf/katalyst-core/pkg/controller/overcommit/prediction/predictor"
	"github.com/kubewharf/katalyst-core/pkg/controller/overcommit/prediction/provider/prom"
)

var deploymentGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}

func TestInitPredictor(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		conf      *controller.OvercommitConfig
		expectErr bool
	}{
		{
			name: "nsigma",
			conf: &controller.OvercommitConfig{
				Prediction: controller.PredictionConfig{
					Predictor: "NSigma",
				},
			},
			expectErr: false,
		},
		{
			name:      "skip",
			conf:      &controller.OvercommitConfig{},
			expectErr: false,
		},
		{
			name: "unknow",
			conf: &controller.OvercommitConfig{
				Prediction: controller.PredictionConfig{
					Predictor: "test",
				},
			},
			expectErr: true,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := &Prediction{}
			p.conf = tc.conf

			err := p.initPredictor()
			if tc.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestReconcileWorkloads(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name              string
		workload          interface{}
		conf              *controller.OvercommitConfig
		timeSeries        *common.TimeSeries
		predictTimeSeries *common.TimeSeries
		expectRprLen      int
	}{
		{
			name: "patch resource portrait result",
			workload: &v13.Deployment{
				TypeMeta: v12.TypeMeta{
					Kind:       "Deployment",
					APIVersion: "apps/v1",
				},
				ObjectMeta: v12.ObjectMeta{
					Name:      "testDeployment1",
					Namespace: "default",
				},
			},
			conf: &controller.OvercommitConfig{
				Prediction: controller.PredictionConfig{
					MinTimeSeriesDuration: time.Minute,
					NSigmaPredictorConfig: controller.NSigmaPredictorConfig{
						Factor:  3,
						Buckets: 24,
					},
				},
			},
			timeSeries:        generateRandTimeSeries(),
			predictTimeSeries: generatePredictTimeSeries(),
			expectRprLen:      24,
		},
		{
			name: "create resource portrait result",
			workload: &v13.Deployment{
				TypeMeta: v12.TypeMeta{
					Kind:       "Deployment",
					APIVersion: "apps/v1",
				},
				ObjectMeta: v12.ObjectMeta{
					Name:      "testDeployment2",
					Namespace: "default",
				},
			},
			conf: &controller.OvercommitConfig{
				Prediction: controller.PredictionConfig{
					MinTimeSeriesDuration: time.Minute,
					NSigmaPredictorConfig: controller.NSigmaPredictorConfig{
						Factor:  3,
						Buckets: 24,
					},
				},
			},
			timeSeries:        generateRandTimeSeries(),
			predictTimeSeries: generatePredictTimeSeries(),
			expectRprLen:      24,
		},
		{
			name: "validate fail",
			workload: &v13.Deployment{
				TypeMeta: v12.TypeMeta{
					Kind:       "Deployment",
					APIVersion: "apps/v1",
				},
				ObjectMeta: v12.ObjectMeta{
					Name:      "testDeployment3",
					Namespace: "default",
				},
			},
			conf: &controller.OvercommitConfig{
				Prediction: controller.PredictionConfig{
					MinTimeSeriesDuration: 48 * time.Hour,
					NSigmaPredictorConfig: controller.NSigmaPredictorConfig{
						Factor:  3,
						Buckets: 24,
					},
				},
			},
			timeSeries:        generateRandTimeSeries(),
			predictTimeSeries: generatePredictTimeSeries(),
			expectRprLen:      0,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			workload := tc.workload.(*v13.Deployment)
			controlCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{},
				[]runtime.Object{}, []runtime.Object{workload})
			assert.NoError(t, err)

			controller, err := newTestController(context.TODO(), controlCtx, tc.conf)
			assert.NoError(t, err)

			controlCtx.StartInformer(context.TODO())

			controller.provider = prom.NewFakeProvider([]*common.TimeSeries{tc.timeSeries})
			controller.predictor = predictor.NewFakePredictor(tc.predictTimeSeries)

			synced := cache.WaitForCacheSync(context.TODO().Done(), controller.syncedFunc...)
			assert.True(t, synced)

			controller.reconcileWorkloads()

			d := tc.workload.(*v13.Deployment)
			cacheKey := workloadUsageCacheName(d.Namespace, d.Kind, d.Name)
			_, ok := controller.workloadUsageCache[cacheKey]

			if tc.expectRprLen != 0 {
				assert.True(t, ok)
				assert.NotNil(t, controller.workloadUsageCache[cacheKey][v1.ResourceCPU.String()])
				assert.Equal(t, tc.expectRprLen, len(controller.workloadUsageCache[cacheKey][v1.ResourceCPU.String()].Samples))
			} else {
				assert.False(t, ok)
			}
		})
	}
}

func TestDeleteWorkloadUsageCache(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		workload *v13.Deployment
		conf     *controller.OvercommitConfig
	}{
		{
			name: "test1",
			workload: &v13.Deployment{
				TypeMeta: v12.TypeMeta{
					Kind:       "Deployment",
					APIVersion: "apps/v1",
				},
				ObjectMeta: v12.ObjectMeta{
					Name:      "testDeployment",
					Namespace: "default",
				},
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			workload := tc.workload
			controlCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{},
				[]runtime.Object{}, []runtime.Object{workload})
			assert.NoError(t, err)

			controller, err := newTestController(context.TODO(), controlCtx, tc.conf)
			assert.NoError(t, err)
			controller.provider = prom.NewFakeProvider(nil)
			controller.predictor = predictor.NewFakePredictor(nil)

			controller.addDeleteHandler(controlCtx.DynamicResourcesManager.GetDynamicInformers())

			controlCtx.StartInformer(context.TODO())

			synced := cache.WaitForCacheSync(context.TODO().Done(), controller.syncedFunc...)
			assert.True(t, synced)

			err = controlCtx.Client.DynamicClient.Resource(deploymentGVR).Namespace(workload.Namespace).Delete(context.TODO(), workload.Name, v12.DeleteOptions{})
			assert.NoError(t, err)

			_, err = controlCtx.Client.DynamicClient.Resource(deploymentGVR).Namespace(workload.Namespace).Get(context.TODO(), workload.Name, v12.GetOptions{})
			assert.Error(t, err)

			time.Sleep(time.Second)

			cacheKey := workloadUsageCacheName(workload.Namespace, workload.Kind, workload.Name)
			controller.RLock()
			_, ok := controller.workloadUsageCache[cacheKey]
			controller.RUnlock()
			assert.False(t, ok)
		})
	}
}

func newTestController(
	ctx context.Context,
	controlCtx *katalyst_base.GenericContext,
	overcommitConf *controller.OvercommitConfig,
) (*Prediction, error) {
	predictionController := &Prediction{
		ctx:                ctx,
		metricsEmitter:     controlCtx.EmitterPool.GetDefaultMetricsEmitter(),
		conf:               overcommitConf,
		workloadLister:     map[schema.GroupVersionResource]cache.GenericLister{},
		workloadUsageCache: map[string]map[string]*common.TimeSeries{},
	}

	workloadInformers := controlCtx.DynamicResourcesManager.GetDynamicInformers()
	for _, wf := range workloadInformers {
		predictionController.workloadLister[wf.GVR] = wf.Informer.Lister()
		predictionController.syncedFunc = append(predictionController.syncedFunc, wf.Informer.Informer().HasSynced)
	}

	predictionController.spdLister = controlCtx.InternalInformerFactory.Workload().V1alpha1().ServiceProfileDescriptors().Lister()
	predictionController.syncedFunc = append(predictionController.syncedFunc, controlCtx.InternalInformerFactory.Workload().V1alpha1().ServiceProfileDescriptors().Informer().HasSynced)
	predictionController.nodeLister = controlCtx.KubeInformerFactory.Core().V1().Nodes().Lister()
	predictionController.syncedFunc = append(predictionController.syncedFunc, controlCtx.KubeInformerFactory.Core().V1().Nodes().Informer().HasSynced)
	predictionController.podIndexer = controlCtx.KubeInformerFactory.Core().V1().Pods().Informer().GetIndexer()
	predictionController.podIndexer.AddIndexers(cache.Indexers{
		nodePodIndex: nodePodIndexFunc,
	})
	predictionController.nodeUpdater = control.NewRealNodeUpdater(controlCtx.Client.KubeClient)

	return predictionController, nil
}

func generateRandTimeSeries() *common.TimeSeries {
	rand.Seed(time.Now().UnixNano())
	now := time.Now()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	nextDay := day.Add(24 * time.Hour)

	res := common.EmptyTimeSeries()
	for day.Before(nextDay) {
		res.Samples = append(res.Samples, common.Sample{
			Value:     rand.Float64() * 4,
			Timestamp: day.Unix(),
		})

		day = day.Add(1 * time.Minute)
	}

	return res
}

func generatePredictTimeSeries() *common.TimeSeries {
	now := time.Now()
	startTime := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Add(24 * time.Hour)

	res := common.EmptyTimeSeries()
	for i := 0; i < 24; i++ {
		res.Samples = append(res.Samples, common.Sample{
			Value:     rand.Float64() * 4,
			Timestamp: startTime.Unix(),
		})

		startTime = startTime.Add(1 * time.Hour)
	}

	return res
}
