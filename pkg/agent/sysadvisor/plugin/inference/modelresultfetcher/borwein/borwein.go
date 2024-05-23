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
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	//nolint
	"github.com/golang/protobuf/proto"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/modelresultfetcher"
	borweinconsts "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/consts"
	borweininfsvc "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/inferencesvc"
	borweintypes "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/types"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const (
	BorweinModelResultFetcherName = "borwein_model_result_fetcher"

	metricInferenceResponseRatio       = "borwein_inference_response_ratio"
	metricGetInferenceRequestFailed    = "borwein_get_inference_request_failed"
	metricInferenceFailed              = "borwein_inference_failed"
	metricParseInferenceResponseFailed = "borwein_parse_inference_response_failed"
	metricSetInferenceResultFailed     = "borwein_set_inference_result_failed"
	metricOverloadContainerRatio       = "borwein_overload_container_ratio"
)

type BorweinModelResultFetcher struct {
	name      string
	qosConfig *generic.QoSConfiguration

	nodeFeatureNames              []string // handled by GetNodeFeature
	containerFeatureNames         []string // handled by GetContainerFeature
	inferenceServiceSocketAbsPath string

	emitter metrics.MetricEmitter

	infSvcClient borweininfsvc.InferenceServiceClient
	clientLock   sync.RWMutex
}

const (
	NodeFeatureNodeName = "node_feature_name"
)

type (
	GetNodeFeatureValueFunc      func(timestamp int64, featureName string, metaServer *metaserver.MetaServer, metaReader metacache.MetaReader) (string, error)
	GetContainerFeatureValueFunc func(timestamp int64, podUID string, containerName string, featureName string,
		metaServer *metaserver.MetaServer, metaReader metacache.MetaReader) (string, error)
)

var (
	getNodeFeatureValue      GetNodeFeatureValueFunc      = nativeGetNodeFeatureValue
	getContainerFeatureValue GetContainerFeatureValueFunc = nativeGetContainerFeatureValue
)

// RegisterGetNodeFeatureValueFunc allows to register pluggable function providing node features
func SetGetNodeFeatureValueFunc(f GetNodeFeatureValueFunc) {
	getNodeFeatureValue = f
}

// RegisterGetContainerFeatureValueFunc allows to register pluggable function providing container features
func SetGetContainerFeatureValueFunc(f GetContainerFeatureValueFunc) {
	getContainerFeatureValue = f
}

// Registered from adapter
func nativeGetNodeFeatureValue(timestamp int64, featureName string, metaServer *metaserver.MetaServer, metaReader metacache.MetaReader) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	node, err := metaServer.GetNode(ctx)
	if err != nil {
		return "", fmt.Errorf("GetNode failed with error: %v", err)
	}

	switch featureName {
	case NodeFeatureNodeName:
		return node.Name, nil
	default:
		return "", fmt.Errorf("unsupported feature: %s", featureName)
	}
}

func nativeGetContainerFeatureValue(timestamp int64, podUID string, containerName string, featureName string,
	metaServer *metaserver.MetaServer, metaReader metacache.MetaReader,
) (string, error) {
	switch featureName {
	case consts.MetricCPUUsageContainer:
		data, err := metaReader.GetContainerMetric(podUID, containerName, featureName)
		if err != nil {
			return "", fmt.Errorf("GetContainerMetric failed with error: %v", err)
		}

		return fmt.Sprintf("%f", data.Value), nil
	default:
		return "", fmt.Errorf("unsupported feature: %s", featureName)
	}
}

func (bmrf *BorweinModelResultFetcher) FetchModelResult(ctx context.Context, metaReader metacache.MetaReader,
	metaWriter metacache.MetaWriter, metaServer *metaserver.MetaServer,
) error {
	bmrf.clientLock.RLock()
	if bmrf.infSvcClient == nil {
		bmrf.clientLock.RUnlock()
		return fmt.Errorf("infSvcClient isn't initialized")
	}
	bmrf.clientLock.RUnlock()

	requestContainers := []*types.ContainerInfo{}
	metaReader.RangeContainer(func(podUID string, containerName string, containerInfo *types.ContainerInfo) (ret bool) {
		ret = true

		if containerInfo == nil {
			general.Warningf("pod: %s, container: %s has nil containerInfo", podUID, containerName)
			return
		} else if containerInfo.ContainerType != v1alpha1.ContainerType_MAIN {
			// todo: currently only inference for main container,
			// maybe supporting sidecar later
			return
		}

		if containerInfo.QoSLevel == apiconsts.PodAnnotationQoSLevelSharedCores || containerInfo.IsDedicatedNumaExclusive() {
			requestContainers = append(requestContainers, containerInfo.Clone())
		}

		return
	})

	if len(requestContainers) == 0 {
		general.Warningf("there is no target container to inference for")
		return nil
	}

	req, err := bmrf.getInferenceRequestForPods(requestContainers, metaReader, metaWriter, metaServer)
	if err != nil {
		_ = bmrf.emitter.StoreInt64(metricGetInferenceRequestFailed, 1, metrics.MetricTypeNameRaw)
		return fmt.Errorf("getInferenceRequestForPods failed with error: %v", err)
	}

	bmrf.clientLock.RLock()
	resp, err := bmrf.infSvcClient.Inference(ctx, req)
	bmrf.clientLock.RUnlock()

	if err != nil {
		_ = bmrf.emitter.StoreInt64(metricInferenceFailed, 1, metrics.MetricTypeNameRaw)
		return fmt.Errorf("Inference failed with error: %v", err)
	}

	borweinInferenceResults, err := bmrf.parseInferenceRespForPods(requestContainers, resp)
	if err != nil {
		_ = bmrf.emitter.StoreInt64(metricParseInferenceResponseFailed, 1, metrics.MetricTypeNameRaw)
		return fmt.Errorf("parseInferenceRespForPods failed with error: %v", err)
	}

	err = metaWriter.SetInferenceResult(borweinconsts.ModelNameBorwein, borweinInferenceResults)
	if err != nil {
		_ = bmrf.emitter.StoreInt64(metricSetInferenceResultFailed, 1, metrics.MetricTypeNameRaw)
		return fmt.Errorf("SetInferenceResult failed with error: %v", err)
	}

	return nil
}

func (bmrf *BorweinModelResultFetcher) parseInferenceRespForPods(requestContainers []*types.ContainerInfo,
	resp *borweininfsvc.InferenceResponse,
) (*borweintypes.BorweinInferenceResults, error) {
	if resp == nil || resp.PodResponseEntries == nil {
		return nil, fmt.Errorf("nil resp")
	}

	results := borweintypes.NewBorweinInferenceResults()
	// Typically the time diff between "call inference" and "get results" could be ignored.
	results.Timestamp = time.Now().UnixMilli()
	respContainersCnt := 0

	for podUID, containerEntries := range resp.PodResponseEntries {
		if containerEntries == nil || len(containerEntries.ContainerInferenceResults) == 0 {
			return nil, fmt.Errorf("invalid containerEntries for pod: %s", podUID)
		}

		for containerName, cResults := range containerEntries.ContainerInferenceResults {
			if cResults == nil {
				return nil, fmt.Errorf("invalid result for pod: %s, container: %s", podUID, containerName)
			}

			inferenceResults := make([]*borweininfsvc.InferenceResult, len(cResults.InferenceResults))
			for idx, result := range cResults.InferenceResults {
				if result == nil {
					continue
				}

				inferenceResults[idx] = proto.Clone(result).(*borweininfsvc.InferenceResult)
			}

			results.SetInferenceResults(podUID, containerName, inferenceResults...)
		}

		respContainersCnt += len(results.Results[podUID])
	}

	overloadCnt := 0.0
	results.RangeInferenceResults(func(_, _ string, result *borweininfsvc.InferenceResult) {
		switch result.InferenceType {
		case borweininfsvc.InferenceType_ClassificationOverload:
			if result.IsDefault {
				return
			}
			if result.Output > result.Percentile {
				overloadCnt += 1.0
			}
		}
	})

	if len(requestContainers) > 0 {
		_ = bmrf.emitter.StoreFloat64(metricInferenceResponseRatio, float64(respContainersCnt)/float64(len(requestContainers)), metrics.MetricTypeNameRaw)
		_ = bmrf.emitter.StoreFloat64(metricOverloadContainerRatio, overloadCnt/float64(len(requestContainers)), metrics.MetricTypeNameRaw)
	} else {
		_ = bmrf.emitter.StoreFloat64(metricInferenceResponseRatio, -1, metrics.MetricTypeNameRaw)
	}

	if respContainersCnt != len(requestContainers) {
		return nil, fmt.Errorf("count of resp containers: %d and request containers: %d are not same",
			respContainersCnt, len(requestContainers))
	}

	return results, nil
}

func (bmrf *BorweinModelResultFetcher) getInferenceRequestForPods(requestContainers []*types.ContainerInfo, metaReader metacache.MetaReader,
	metaWriter metacache.MetaWriter, metaServer *metaserver.MetaServer,
) (*borweininfsvc.InferenceRequest, error) {
	if getNodeFeatureValue == nil {
		return nil, fmt.Errorf("nil getNodeFeatureValue")
	}

	if getContainerFeatureValue == nil {
		return nil, fmt.Errorf("nil getContainerFeatureValue")
	}

	callTimestampInSec := time.Now().Unix()
	req := &borweininfsvc.InferenceRequest{
		FeatureNames:      make([]string, 0, len(bmrf.nodeFeatureNames)+len(bmrf.containerFeatureNames)),
		PodRequestEntries: make(map[string]*borweininfsvc.ContainerRequestEntries),
	}

	req.FeatureNames = append(req.FeatureNames, bmrf.nodeFeatureNames...)
	req.FeatureNames = append(req.FeatureNames, bmrf.containerFeatureNames...)

	nodeFeatureValues := make([]string, 0, len(bmrf.nodeFeatureNames))
	for _, nodeFeatureName := range bmrf.nodeFeatureNames {
		nodeFeatureValue, err := getNodeFeatureValue(callTimestampInSec, nodeFeatureName, metaServer, metaReader)
		if err != nil {
			return nil, fmt.Errorf("get node feature: %v failed with error: %v", nodeFeatureName, err)
		}

		nodeFeatureValues = append(nodeFeatureValues, nodeFeatureValue)
	}

	for _, containerInfo := range requestContainers {
		if containerInfo == nil {
			general.Warningf("nil containerInfo")
			continue
		}

		unionFeatureValues := &borweininfsvc.FeatureValues{
			Values: make([]string, 0, len(req.FeatureNames)),
		}

		unionFeatureValues.Values = append(unionFeatureValues.Values, nodeFeatureValues...)

		for _, containerFeatureName := range bmrf.containerFeatureNames {
			containerFeatureValue, err := getContainerFeatureValue(callTimestampInSec,
				containerInfo.PodUID,
				containerInfo.ContainerName,
				containerFeatureName,
				metaServer, metaReader)
			// Let getContainerFeatureValue decide which feature is allowed to return default value.
			if err != nil {
				return nil, fmt.Errorf("getContainerFeatureValue for pod: %s/%s, container: %s failed, err: %v",
					containerInfo.PodNamespace, containerInfo.PodName, containerInfo.ContainerName, err)
			}

			unionFeatureValues.Values = append(unionFeatureValues.Values, containerFeatureValue)
		}

		if req.PodRequestEntries[containerInfo.PodUID] == nil {
			req.PodRequestEntries[containerInfo.PodUID] = &borweininfsvc.ContainerRequestEntries{
				ContainerFeatureValues: make(map[string]*borweininfsvc.FeatureValues),
			}
		}

		req.PodRequestEntries[containerInfo.PodUID].ContainerFeatureValues[containerInfo.ContainerName] = unionFeatureValues
	}

	return req, nil
}

// initAdvisorClientConn initializes memory-advisor related connections
func (bmrf *BorweinModelResultFetcher) initInferenceSvcClientConn() (bool, error) {
	// todo: emit metrics when initializing client connection failed

	// never success
	if bmrf.inferenceServiceSocketAbsPath == "" {
		return false, fmt.Errorf("empty inferenceServiceSocketAbsPath")
	}

	infSvcConn, err := process.Dial(bmrf.inferenceServiceSocketAbsPath, 5*time.Second)
	if err != nil {
		general.Errorf("get inference svc connection with socket: %s failed with error: %v", bmrf.inferenceServiceSocketAbsPath, err)
		return false, nil
	}

	bmrf.clientLock.Lock()
	bmrf.infSvcClient = borweininfsvc.NewInferenceServiceClient(infSvcConn)
	bmrf.clientLock.Unlock()
	return true, nil
}

func NewBorweinModelResultFetcher(fetcherName string, conf *config.Configuration, extraConf interface{},
	emitterPool metricspool.MetricsEmitterPool, metaServer *metaserver.MetaServer,
	metaCache metacache.MetaCache,
) (modelresultfetcher.ModelResultFetcher, error) {
	if conf == nil || conf.BorweinConfiguration == nil {
		return nil, fmt.Errorf("nil conf")
	} else if !conf.PolicyRama.EnableBorweinModelResultFetcher {
		return nil, nil
	} else if metaServer == nil {
		return nil, fmt.Errorf("nil metaServer")
	} else if metaCache == nil {
		return nil, fmt.Errorf("nil metaCache")
	}

	emitter := emitterPool.GetDefaultMetricsEmitter().WithTags(BorweinModelResultFetcherName)

	bmrf := &BorweinModelResultFetcher{
		name:                          fetcherName,
		emitter:                       emitter,
		qosConfig:                     conf.QoSConfiguration,
		nodeFeatureNames:              conf.BorweinConfiguration.NodeFeatureNames,
		containerFeatureNames:         conf.BorweinConfiguration.ContainerFeatureNames,
		inferenceServiceSocketAbsPath: conf.BorweinConfiguration.InferenceServiceSocketAbsPath,
	}

	// fetcher initializing doesn't block sys-adviosr main process
	go func() {
		err := wait.PollImmediateInfinite(5*time.Second, bmrf.initInferenceSvcClientConn)
		if err != nil {
			general.Fatalf("polling to connect borwein inference server failed with error: %v", err)
		}

		general.Infof("connect borwein inference server successfully")
	}()

	return bmrf, nil
}
