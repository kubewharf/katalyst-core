package kubeletconfig

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"

	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const (
	defaultNodeAddress = "127.0.0.1"

	metricsNameKubeletConfigCacheSync = "kubelet_config_cache_sync"
)

// KubeletConfigFetcher is used to get the configuration of kubelet.
type KubeletConfigFetcher interface {
	// Run starts the preparing logic to get kubelet config.
	Run(ctx context.Context)

	// GetKubeletConfig returns the configuration of kubelet.
	GetKubeletConfig(ctx context.Context) (*kubeletconfigv1beta1.KubeletConfiguration, error)
}

// NewKubeletConfigFetcher returns a KubeletConfigFetcher
func NewKubeletConfigFetcher(conf *config.Configuration, emitter metrics.MetricEmitter) KubeletConfigFetcher {
	nodeAddress := os.Getenv("NODE_ADDRESS")
	if nodeAddress == "" {
		nodeAddress = defaultNodeAddress
	}

	return &kubeletConfigFetcherImpl{
		emitter:  emitter,
		conf:     conf,
		endpoint: fmt.Sprintf("https://%s:%d%s", nodeAddress, conf.KubeletSecurePort, conf.KubeletConfigURI),
	}
}

// kubeletConfigFetcherImpl use kubelet 10255 pods interface to get pod directly without cache.
type kubeletConfigFetcherImpl struct {
	sync.RWMutex

	emitter  metrics.MetricEmitter
	endpoint string
	conf     *config.Configuration

	kubeletConfig *kubeletconfigv1beta1.KubeletConfiguration
}

// Run starts a kubeletConfigFetcherImpl
func (k *kubeletConfigFetcherImpl) Run(ctx context.Context) {
	go wait.UntilWithContext(ctx, k.syncKubeletConfig, k.conf.KubeletConfigCacheSyncPeriod)
	<-ctx.Done()
}

// GetKubeletConfig gets kubelet config from kubelet 10250/configz api
func (k *kubeletConfigFetcherImpl) GetKubeletConfig(ctx context.Context) (*kubeletconfigv1beta1.KubeletConfiguration, error) {
	k.RLock()
	defer k.RUnlock()

	if k.kubeletConfig != nil {
		return k.kubeletConfig, nil
	}

	configFresh, err := k.getKubeletConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetKubeletConfig failed, err: %v", err)
	}

	if configFresh == nil {
		return nil, errors.New("GetKubeletConfig failed because getKubeletConfig got nil")
	}

	return configFresh, nil
}

// syncKubeletConfig sync local kubelet pod cache from kubelet pod fetcher.
func (k *kubeletConfigFetcherImpl) syncKubeletConfig(ctx context.Context) {
	kubeletConfig, err := k.getKubeletConfig(ctx)
	if err != nil {
		klog.Errorf("sync kubelet config failed: %v", err)
		_ = k.emitter.StoreInt64(metricsNameKubeletConfigCacheSync, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				"source":  "kubelet",
				"success": "false",
				"reason":  "error",
			})...)
		return
	}
	if kubeletConfig == nil {
		klog.Error("kubelet config is nil")
		_ = k.emitter.StoreInt64(metricsNameKubeletConfigCacheSync, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				"source":  "kubelet",
				"success": "false",
				"reason":  "empty",
			})...)
		return
	}

	_ = k.emitter.StoreInt64(metricsNameKubeletConfigCacheSync, 1, metrics.MetricTypeNameCount,
		metrics.ConvertMapToTags(map[string]string{
			"source":  "kubelet",
			"success": "true",
		})...)

	k.Lock()
	k.kubeletConfig = kubeletConfig
	k.Unlock()
}

// getKubeletConfig gets kubelet config from kubelet 10250/configz api
func (k *kubeletConfigFetcherImpl) getKubeletConfig(ctx context.Context) (*kubeletconfigv1beta1.KubeletConfiguration, error) {
	u, err := url.ParseRequestURI(k.endpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to parse -kubelet-config-uri: %w", err)
	}

	restConfig, err := process.InsecureConfig(u.String(), k.conf.APIAuthTokenFile)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize rest config for kubelet config uri: %w", err)
	}

	type configzWrapper struct {
		ComponentConfig kubeletconfigv1beta1.KubeletConfiguration `json:"kubeletconfig"`
	}
	configz := configzWrapper{}

	if err := process.GetAndUnmarshalForHttps(ctx, restConfig, &configz); err != nil {
		return nil, fmt.Errorf("failed to get kubelet config, error: %v", err)
	}

	return &configz.ComponentConfig, nil
}
