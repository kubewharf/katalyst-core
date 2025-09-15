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

package borwein

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	inferenceConsts "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/modelinputfetcher"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
)

const (
	BorweinModelInputFetcherName = "borwein_model_input_fetcher"

	NodeFeatureNodeName = "node_feature_name"
)

type (
	GetNodeFeatureValueFunc      func(timestamp int64, metaServer *metaserver.MetaServer, metaReader metacache.MetaReader, conf *config.Configuration, extraConf interface{}) (map[string]interface{}, error)
	GetNumaFeatureValueFunc      func(timestamp int64, numaID int, metaServer *metaserver.MetaServer, metaReader metacache.MetaReader, conf *config.Configuration, extraConf interface{}) (map[string]interface{}, error)
	GetContainerFeatureValueFunc func(timestamp int64, podUID string, containerName string, metaServer *metaserver.MetaServer, metaReader metacache.MetaReader, conf *config.Configuration, extraConf interface{}) (map[string]interface{}, error)
)

var (
	getNodeFeatureValue      GetNodeFeatureValueFunc      = nativeGetNodeFeatureValue
	getNumaFeatureValue      GetNumaFeatureValueFunc      = nativeGetNumaFeatureValue
	getContainerFeatureValue GetContainerFeatureValueFunc = nativeGetContainerFeatureValue
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

// Registered from adapter
func nativeGetNodeFeatureValue(timestamp int64, metaServer *metaserver.MetaServer, metaReader metacache.MetaReader, conf *config.Configuration, extraConf interface{}) (map[string]interface{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	node, err := metaServer.GetNode(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetNode failed with error: %v", err)
	}
	features := make(map[string]interface{})
	features[NodeFeatureNodeName] = node.Name

	return features, nil
}

func nativeGetNumaFeatureValue(timestamp int64, numaID int, metaServer *metaserver.MetaServer, metaReader metacache.MetaReader, conf *config.Configuration, extraConf interface{}) (map[string]interface{}, error) {
	numaFeatures := make(map[string]interface{})

	metricName := consts.MetricMemBandwidthNuma
	data, err := metaReader.GetNumaMetric(numaID, metricName)
	if err != nil {
		return nil, fmt.Errorf("GetNumaMetric for numa %d feature %s failed with error: %v", numaID, metricName, err)
	}
	numaFeatures[metricName] = data.Value

	return numaFeatures, nil
}

func nativeGetContainerFeatureValue(timestamp int64, podUID string, containerName string, metaServer *metaserver.MetaServer, metaReader metacache.MetaReader, conf *config.Configuration, extraConf interface{},
) (map[string]interface{}, error) {
	podFeatures := make(map[string]interface{})

	featureName := consts.MetricCPUUsageContainer
	containerFeatures := make(map[string]interface{})
	data, err := metaReader.GetContainerMetric(podUID, containerName, featureName)
	if err != nil {
		return nil, fmt.Errorf("GetContainerMetric for pod %s container %s feature %s failed with error: %v", podUID, containerName, featureName, err)
	}
	containerFeatures[featureName] = data.Value
	podFeatures[containerName] = containerFeatures

	return podFeatures, nil
}

type BorweinModelInputFetcher struct {
	name      string
	conf      *config.Configuration
	extraConf interface{}
	qosConfig *generic.QoSConfiguration
	emitter   metrics.MetricEmitter
}

func NewBorweinModelInputFetcher(fetcherName string, conf *config.Configuration, extraConf interface{},
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

	emitter := emitterPool.GetDefaultMetricsEmitter().WithTags(BorweinModelInputFetcherName)

	bmif := &BorweinModelInputFetcher{
		name:      fetcherName,
		emitter:   emitter,
		conf:      conf,
		extraConf: extraConf,
		qosConfig: conf.QoSConfiguration,
	}

	return bmif, nil
}

func (bmif *BorweinModelInputFetcher) FetchModelInput(ctx context.Context, metaReader metacache.MetaReader,
	metaWriter metacache.MetaWriter, metaServer *metaserver.MetaServer,
) error {
	var errs []error
	if getNodeFeatureValue == nil {
		errs = append(errs, fmt.Errorf("nil getNodeFeatureValue"))
	}
	if getNumaFeatureValue == nil {
		errs = append(errs, fmt.Errorf("nil getNumaFeatureValue"))
	}
	if getContainerFeatureValue == nil {
		errs = append(errs, fmt.Errorf("nil getContainerFeatureValue"))
	}

	callTimestampInSec := time.Now().Unix()
	NodeInfo, err := getNodeFeatureValue(callTimestampInSec, metaServer, metaReader, bmif.conf, bmif.extraConf)
	if err != nil {
		errs = append(errs, fmt.Errorf("get %v feature failed with error: %w", inferenceConsts.MetricDimensionNode, err))
	}

	NumaInfo := make(map[string]interface{})
	for numaID := 0; numaID < metaServer.NumNUMANodes; numaID++ {
		numaFeatureValues, err := getNumaFeatureValue(callTimestampInSec, numaID, metaServer, metaReader, bmif.conf, bmif.extraConf)
		if err != nil {
			errs = append(errs, fmt.Errorf("get %v feature failed with error: %w", inferenceConsts.MetricDimensionNuma, err))
		}
		NumaInfo[strconv.Itoa(numaID)] = numaFeatureValues
	}

	PodInfo := make(map[string]interface{})
	// map[PodUID]map[ContainerName]map[featureName]featureValue
	pods, err := metaServer.GetPodList(context.Background(), func(pod *v1.Pod) bool { return true })
	if err != nil {
		errs = append(errs, fmt.Errorf("GetPodList failed with error: %w", err))
	}
	for _, pod := range pods {
		if pod == nil {
			continue
		}
		podFeature := make(map[string]map[string]interface{})
		for _, container := range pod.Spec.Containers {
			containerInfo, err := getContainerFeatureValue(callTimestampInSec, string(pod.UID), container.Name, metaServer, metaReader, bmif.conf, bmif.extraConf)
			if err != nil {
				errs = append(errs, fmt.Errorf("get %v feature failed with error: %w", inferenceConsts.MetricDimensionContainer, err))
			}
			podFeature[container.Name] = containerInfo
		}
		PodInfo[string(pod.UID)] = podFeature
	}

	metaWriter.SetModelInput(inferenceConsts.MetricDimensionNode, NodeInfo)
	metaWriter.SetModelInput(inferenceConsts.MetricDimensionContainer, PodInfo)
	metaWriter.SetModelInput(inferenceConsts.MetricDimensionNuma, NumaInfo)

	if len(errs) > 0 {
		var sb strings.Builder
		sb.WriteString("multiple errors occurred while fetching model input: ")
		for i, e := range errs {
			if i > 0 {
				sb.WriteString("; ")
			}
			sb.WriteString(e.Error())
		}
		return fmt.Errorf(sb.String())
	}

	return nil
}
