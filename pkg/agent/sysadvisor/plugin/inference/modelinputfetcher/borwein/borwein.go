package borwein

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	inferenceConsts "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/modelinputfetcher"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	v1 "k8s.io/api/core/v1"
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
	return nil, fmt.Errorf("not implemented")
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
	if conf == nil || conf.BorweinConfiguration == nil {
		return nil, fmt.Errorf("nil conf")
	} else if !conf.PolicyRama.EnableBorweinModelResultFetcher {
		return nil, nil
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
	metaWriter metacache.MetaWriter, metaServer *metaserver.MetaServer) error {
	if getNodeFeatureValue == nil {
		return fmt.Errorf("nil getNodeFeatureValue")
	}
	if getNumaFeatureValue == nil {
		return fmt.Errorf("nil getNumaFeatureValue")
	}
	if getContainerFeatureValue == nil {
		return fmt.Errorf("nil getContainerFeatureValue")
	}

	callTimestampInSec := time.Now().Unix()
	NodeInfo, err := getNodeFeatureValue(callTimestampInSec, metaServer, metaReader, bmif.conf, bmif.extraConf)
	if err != nil {
		return fmt.Errorf("get %v feature failed with error: %v", inferenceConsts.MetricDimensionNode, err)
	}

	NumaInfo := make(map[string]interface{})
	for numaID := 0; numaID < metaServer.NumNUMANodes; numaID++ {
		numaFeatureValues, err := getNumaFeatureValue(callTimestampInSec, numaID, metaServer, metaReader, bmif.conf, bmif.extraConf)
		if err != nil {
			return fmt.Errorf("get %v feature failed with error: %v", inferenceConsts.MetricDimensionNuma, err)
		}
		NumaInfo[strconv.Itoa(numaID)] = numaFeatureValues
	}

	PodInfo := make(map[string]interface{})
	// map[PodUID]map[ContainerName]map[featureName]featureValue
	pods, err := metaServer.GetPodList(context.Background(), func(pod *v1.Pod) bool { return true })
	if err != nil {
		return fmt.Errorf("GetPodList failed with error: %v", err)
	}
	for _, pod := range pods {
		podFeature := make(map[string]map[string]interface{})
		for _, container := range pod.Spec.Containers {
			containerInfo, err := getContainerFeatureValue(callTimestampInSec, string(pod.UID), container.Name, metaServer, metaReader, bmif.conf, bmif.extraConf)
			if err != nil {
				return fmt.Errorf("get %v feature failed with error: %v", inferenceConsts.MetricDimensionPod, err)
			}
			podFeature[container.Name] = containerInfo
		}
		PodInfo[string(pod.UID)] = podFeature
	}

	metaWriter.SetModelInput(inferenceConsts.MetricDimensionNode, NodeInfo)
	metaWriter.SetModelInput(inferenceConsts.MetricDimensionPod, PodInfo)
	metaWriter.SetModelInput(inferenceConsts.MetricDimensionNuma, NumaInfo)

	return nil
}
