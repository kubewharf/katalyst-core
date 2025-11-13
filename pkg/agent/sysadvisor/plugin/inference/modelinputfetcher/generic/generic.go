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

package generic

import (
	"context"
	"fmt"
	"strconv"
	"time"

	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	inferenceConsts "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/modelinputfetcher"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	GenericModelInputFetcherName = "generic_model_input_fetcher"
)

type (
	GetNodeFeatureValueFunc      func(timestamp int64, metaServer *metaserver.MetaServer, metaReader metacache.MetaReader, conf *config.Configuration, extraConf interface{}) (map[string]interface{}, error)
	GetNumaFeatureValueFunc      func(timestamp int64, numaID int, metaServer *metaserver.MetaServer, metaReader metacache.MetaReader, conf *config.Configuration, extraConf interface{}) (map[string]interface{}, error)
	GetContainerFeatureValueFunc func(timestamp int64, podUID string, containerName string, metaServer *metaserver.MetaServer, metaReader metacache.MetaReader, conf *config.Configuration, extraConf interface{}) (map[string]interface{}, error)
)

var (
	getNodeFeatureValue      GetNodeFeatureValueFunc
	getNumaFeatureValue      GetNumaFeatureValueFunc
	getContainerFeatureValue GetContainerFeatureValueFunc
)

// RegisterGetNodeFeatureValueFunc allows to register pluggable function providing node features
func SetGetNodeFeatureValueFunc(f GetNodeFeatureValueFunc) {
	getNodeFeatureValue = f
}

// RegisterGetNumaFeatureValueFunc allows to register pluggable function providing numa features
func SetGetNumaFeatureValueFunc(f GetNumaFeatureValueFunc) {
	getNumaFeatureValue = f
}

// RegisterGetContainerFeatureValueFunc allows to register pluggable function providing container features
func SetGetContainerFeatureValueFunc(f GetContainerFeatureValueFunc) {
	getContainerFeatureValue = f
}

type GenericModelInputFetcher struct {
	name      string
	conf      *config.Configuration
	extraConf interface{}
	qosConfig *generic.QoSConfiguration
	emitter   metrics.MetricEmitter
}

func NewGenericModelInputFetcher(fetcherName string, conf *config.Configuration, extraConf interface{},
	emitterPool metricspool.MetricsEmitterPool, metaServer *metaserver.MetaServer,
	metaCache metacache.MetaCache,
) (modelinputfetcher.ModelInputFetcher, error) {
	if conf == nil {
		return nil, fmt.Errorf("nil conf")
	} else if metaServer == nil {
		return nil, fmt.Errorf("nil metaServer")
	} else if metaCache == nil {
		return nil, fmt.Errorf("nil metaCache")
	}

	emitter := emitterPool.GetDefaultMetricsEmitter().WithTags(GenericModelInputFetcherName)

	bmif := &GenericModelInputFetcher{
		name:      fetcherName,
		emitter:   emitter,
		conf:      conf,
		extraConf: extraConf,
		qosConfig: conf.QoSConfiguration,
	}

	return bmif, nil
}

func (bmif *GenericModelInputFetcher) FetchModelInput(ctx context.Context, metaReader metacache.MetaReader,
	metaWriter metacache.MetaWriter, metaServer *metaserver.MetaServer,
) error {
	var nodeErrs, numaErrs, containerErrs []error

	callTimestampInSec := time.Now().Unix()
	if getNodeFeatureValue == nil {
		return fmt.Errorf("getNodeFeatureValue is nil")
	}
	if getNumaFeatureValue == nil {
		return fmt.Errorf("getNumaFeatureValue is nil")
	}
	if getContainerFeatureValue == nil {
		return fmt.Errorf("getContainerFeatureValue is nil")
	}
	NodeInfo, err := getNodeFeatureValue(callTimestampInSec, metaServer, metaReader, bmif.conf, bmif.extraConf)
	if err != nil {
		nodeErrs = append(nodeErrs, fmt.Errorf("get %v feature failed with error: %w", inferenceConsts.MetricDimensionNode, err))
	}

	NumaInfo := make(map[string]interface{})
	for numaID := 0; numaID < metaServer.NumNUMANodes; numaID++ {
		numaFeatureValues, err := getNumaFeatureValue(callTimestampInSec, numaID, metaServer, metaReader, bmif.conf, bmif.extraConf)
		if err != nil {
			numaErrs = append(numaErrs, fmt.Errorf("get %v feature failed with error: %w", inferenceConsts.MetricDimensionNuma, err))
		}
		NumaInfo[strconv.Itoa(numaID)] = numaFeatureValues
	}

	PodInfo := make(map[string]interface{})
	// map[PodUID]map[ContainerName]map[featureName]featureValue
	pods, err := metaServer.GetPodList(context.Background(), func(pod *v1.Pod) bool { return true })
	if err != nil {
		general.Infof("GetPodList failed with error: %v", err)
	}
	for _, pod := range pods {
		if pod == nil {
			continue
		}
		podFeature := make(map[string]map[string]interface{})
		for _, container := range pod.Spec.Containers {
			containerInfo, err := getContainerFeatureValue(callTimestampInSec, string(pod.UID), container.Name, metaServer, metaReader, bmif.conf, bmif.extraConf)
			if err != nil {
				containerErrs = append(containerErrs, fmt.Errorf("get %v feature failed with error: %w", inferenceConsts.MetricDimensionContainer, err))
			}
			podFeature[container.Name] = containerInfo
		}
		PodInfo[string(pod.UID)] = podFeature
	}

	if err = metaWriter.SetModelInput(inferenceConsts.MetricDimensionNode, NodeInfo); err != nil {
		nodeErrs = append(nodeErrs, fmt.Errorf("set %v input failed with error: %w", inferenceConsts.MetricDimensionNode, err))
		general.Infof("set %v model input failed with error: %v", inferenceConsts.MetricDimensionNode, err)
	}
	if err = metaWriter.SetModelInput(inferenceConsts.MetricDimensionContainer, PodInfo); err != nil {
		containerErrs = append(containerErrs, fmt.Errorf("set %v input failed with error: %w", inferenceConsts.MetricDimensionContainer, err))
		general.Infof("set %v model input failed with error: %v", inferenceConsts.MetricDimensionContainer, err)
	}
	if err = metaWriter.SetModelInput(inferenceConsts.MetricDimensionNuma, NumaInfo); err != nil {
		numaErrs = append(numaErrs, fmt.Errorf("set %v input failed with error: %w", inferenceConsts.MetricDimensionNuma, err))
		general.Infof("set %v model input failed with error: %v", inferenceConsts.MetricDimensionNuma, err)
	}

	if len(nodeErrs)+len(numaErrs)+len(containerErrs) > 0 {
		return fmt.Errorf("multiple errors occurred while fetching model input: %d node err, %d numa err, %d container err",
			len(nodeErrs), len(numaErrs), len(containerErrs))
	}
	return nil
}
