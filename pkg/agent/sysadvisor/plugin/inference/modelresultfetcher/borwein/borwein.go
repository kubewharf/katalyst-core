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
	"time"

	//nolint
	"github.com/golang/protobuf/proto"
	v1 "k8s.io/api/core/v1"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/modelresultfetcher"
	borweinconsts "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/consts"
	borweininfsvc "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/inferencesvc"
	borweintypes "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
	"github.com/kubewharf/katalyst-core/pkg/util/qos"
)

const (
	BorweinModelResultFetcherName = "borwein_model_result_fetcher"
)

type BorweinModelResultFetcher struct {
	name      string
	qosConfig *generic.QoSConfiguration

	nodeFeatureNames              []string // handled by GetNodeFeature
	containerFeatureNames         []string // handled by GetContainerFeature
	inferenceServiceSocketAbsPath string

	//metaServer *metaserver.MetaServer
	//metaWriter metacache.MetaWriter
	//metaReader metacache.MetaReader

	infSvcClient borweininfsvc.InferenceServiceClient
}

const (
	NodeFeatureNodeName = "node_feature_name"
)

type GetNodeFeatureValueFunc func(featureName string, metaServer *metaserver.MetaServer, metaReader metacache.MetaReader) (string, error)
type GetContainerFeatureValueFunc func(podUID string, containerName string, featureName string,
	metaServer *metaserver.MetaServer, metaReader metacache.MetaReader) (string, error)

var GetNodeFeatureValue GetNodeFeatureValueFunc = NativeGetNodeFeatureValue
var GetContainerFeatureValue GetContainerFeatureValueFunc = NativeGetContainerFeatureValue

// Register func from adapter
func SetGetNodeFeatureValueFunc(f GetNodeFeatureValueFunc) {
	GetNodeFeatureValue = f
}

// Register func from adapter
func SetGetContainerFeatureValueFunc(f GetContainerFeatureValueFunc) {
	GetContainerFeatureValue = f
}

// Registered from adapter
func NativeGetNodeFeatureValue(featureName string, metaServer *metaserver.MetaServer, metaReader metacache.MetaReader) (string, error) {
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

func NativeGetContainerFeatureValue(podUID string, containerName string, featureName string,
	metaServer *metaserver.MetaServer, metaReader metacache.MetaReader) (string, error) {

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
	metaWriter metacache.MetaWriter, metaServer *metaserver.MetaServer) error {

	pods, err := metaServer.GetPodList(ctx, func(pod *v1.Pod) bool {
		if pod == nil {
			return false
		}

		if qos.IsPodNumaExclusive(bmrf.qosConfig, pod) {
			return true
		}

		isSharedQoS, err := bmrf.qosConfig.CheckSharedQoSForPod(pod)

		if err != nil {
			general.Errorf("CheckSharedQoSForPod for pod: %s/%s failed with error: %v", pod.Namespace, pod.Name, err)
			return false
		}

		return isSharedQoS
	})

	if err != nil {
		return fmt.Errorf("GetPodList failed with error: %v", err)
	}

	req, err := bmrf.getInferenceRequestForPods(pods, metaReader, metaWriter, metaServer)

	if err != nil {
		return fmt.Errorf("getInferenceRequestForPods failed with error: %v", err)
	}

	resp, err := bmrf.infSvcClient.Inference(ctx, req)

	if err != nil {
		return fmt.Errorf("Inference failed with error: %v", err)
	}

	borweinInferenceResults, err := bmrf.parseInferenceRespForPods(pods, resp)

	if err != nil {
		return fmt.Errorf("parseInferenceRespForPods failed with error: %v", err)
	}

	metaWriter.SetInferenceResult(borweinconsts.ModelNameBorwein, borweinInferenceResults)
	return nil
}

func (bmrf *BorweinModelResultFetcher) parseInferenceRespForPods(pods []*v1.Pod,
	resp *borweininfsvc.InferenceResponse) (borweintypes.BorweinInferenceResults, error) {

	if resp == nil || resp.PodResponseEntries == nil {
		return nil, fmt.Errorf("nil resp")
	}

	if len(resp.PodResponseEntries) != len(pods) {
		return nil, fmt.Errorf("lenth of resp.PodResponseEntries: %d and input pods list: %d are not same",
			len(resp.PodResponseEntries), len(pods))
	}

	results := make(borweintypes.BorweinInferenceResults)

	for podUID, containerEntries := range resp.PodResponseEntries {
		if containerEntries == nil || len(containerEntries.ContainerInferenceResults) == 0 {
			return nil, fmt.Errorf("invalid containerEntries for pod: %s", podUID)
		}

		results[podUID] = make(map[string]*borweininfsvc.InferenceResult)

		for containerName, result := range containerEntries.ContainerInferenceResults {
			if result == nil {
				return nil, fmt.Errorf("invalid result for pod: %s, container: %s", podUID, containerName)
			}

			results[podUID][containerName] = proto.Clone(result).(*borweininfsvc.InferenceResult)
		}
	}

	return results, nil
}

func (bmrf *BorweinModelResultFetcher) getInferenceRequestForPods(pods []*v1.Pod, metaReader metacache.MetaReader,
	metaWriter metacache.MetaWriter, metaServer *metaserver.MetaServer) (*borweininfsvc.InferenceRequest, error) {
	req := &borweininfsvc.InferenceRequest{
		FeatureNames:      make([]string, 0, len(bmrf.nodeFeatureNames)+len(bmrf.containerFeatureNames)),
		PodRequestEntries: make(map[string]*borweininfsvc.ContainerRequestEntries, len(pods)),
	}

	req.FeatureNames = append(req.FeatureNames, bmrf.nodeFeatureNames...)
	req.FeatureNames = append(req.FeatureNames, bmrf.containerFeatureNames...)

	nodeFeatureValues := make([]string, 0, len(bmrf.nodeFeatureNames))
	for _, nodeFeatureName := range bmrf.nodeFeatureNames {
		if GetNodeFeatureValue == nil {
			return nil, fmt.Errorf("nil GetNodeFeatureValue")
		}

		nodeFeatureValue, err := GetNodeFeatureValue(nodeFeatureName, metaServer, metaReader)

		if err != nil {
			return nil, fmt.Errorf("get node feature: %v failed with error: %v", nodeFeatureName, err)
		}

		nodeFeatureValues = append(nodeFeatureValues, nodeFeatureValue)
	}

	for _, pod := range pods {
		if pod == nil {
			general.Warningf("nil pod")
			continue
		}

		req.PodRequestEntries[string(pod.UID)] = &borweininfsvc.ContainerRequestEntries{
			ContainerFeatureValues: make(map[string]*borweininfsvc.FeatureValues, len(pod.Spec.Containers)),
		}

		foundMainContainer := false
		for i := 0; i < len(pod.Spec.Containers); i++ {
			containerName := pod.Spec.Containers[i].Name
			containerInfo, ok := metaReader.GetContainerInfo(string(pod.UID), containerName)

			if !ok {
				return nil, fmt.Errorf("GetContainerInfo for pod: %s/%s, container: %s failed",
					pod.Namespace, pod.Name, containerName)
			}

			if containerInfo.ContainerType == v1alpha1.ContainerType_MAIN {

				foundMainContainer = true
				unionFeatureValues := &borweininfsvc.FeatureValues{
					Values: make([]string, 0, len(req.FeatureNames)),
				}

				unionFeatureValues.Values = append(unionFeatureValues.Values, nodeFeatureValues...)

				for _, containerFeatureName := range bmrf.containerFeatureNames {
					if GetContainerFeatureValue == nil {
						return nil, fmt.Errorf("nil GetContainerFeatureValue")
					}

					containerFeatureValue, err := GetContainerFeatureValue(string(pod.UID),
						containerName, containerFeatureName,
						metaServer, metaReader)

					if err != nil {
						return nil, fmt.Errorf("GetContainerFeatureValue for pod: %s/%s, container: %s failed",
							pod.Namespace, pod.Name, containerName)
					}

					unionFeatureValues.Values = append(unionFeatureValues.Values, containerFeatureValue)
				}

				req.PodRequestEntries[string(pod.UID)].ContainerFeatureValues[containerName] = unionFeatureValues
				break
			}
		}

		// todo: currently only inference for main container,
		// maybe supporting sidecar later
		if !foundMainContainer {
			return nil, fmt.Errorf("main container isn't found for pod: %s/%s", pod.Namespace, pod.Name)
		}
	}

	return req, nil
}

// initAdvisorClientConn initializes memory-advisor related connections
func (bmrf *BorweinModelResultFetcher) initInferenceSvcClientConn() error {
	if bmrf.inferenceServiceSocketAbsPath == "" {
		return fmt.Errorf("empty inferenceServiceSocketAbsPath")
	}

	infSvcConn, err := process.Dial(bmrf.inferenceServiceSocketAbsPath, 5*time.Second)
	if err != nil {
		return fmt.Errorf("get inference svc connection with socket: %s failed with error: %v", bmrf.inferenceServiceSocketAbsPath, err)
	}

	bmrf.infSvcClient = borweininfsvc.NewInferenceServiceClient(infSvcConn)
	return nil
}

func NewBorweinModelResultFetcher(fetcherName string, conf *config.Configuration, extraConf interface{},
	emitterPool metricspool.MetricsEmitterPool, metaServer *metaserver.MetaServer,
	metaCache metacache.MetaCache) (modelresultfetcher.ModelResultFetcher, error) {
	if conf == nil || conf.BorweinConfiguration == nil {
		return nil, fmt.Errorf("nil conf")
	} else if metaServer == nil {
		return nil, fmt.Errorf("nil metaServer")
	} else if metaCache == nil {
		return nil, fmt.Errorf("nil metaCache")
	}

	bmrf := &BorweinModelResultFetcher{
		name:                          fetcherName,
		qosConfig:                     conf.QoSConfiguration,
		nodeFeatureNames:              conf.BorweinConfiguration.NodeFeatureNames,
		containerFeatureNames:         conf.BorweinConfiguration.ContainerFeatureNames,
		inferenceServiceSocketAbsPath: conf.BorweinConfiguration.InferenceServiceSocketAbsPath,
	}

	err := bmrf.initInferenceSvcClientConn()

	if err != nil {
		return nil, fmt.Errorf("initInferenceSvcClientConn failed with error: %v", err)
	}

	return bmrf, nil
}
