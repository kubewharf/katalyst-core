package kubeletconfig

import (
	"context"
	"fmt"
	"net/url"
	"os"

	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"

	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const (
	defaultNodeAddress = "127.0.0.1"
)

// KubeletConfigFetcher is used to get the configuration of kubelet.
type KubeletConfigFetcher interface {
	// GetKubeletConfig returns the configuration of kubelet.
	GetKubeletConfig(ctx context.Context) (*kubeletconfigv1beta1.KubeletConfiguration, error)
}

// NewKubeletConfigFetcher returns a KubeletConfigFetcher
func NewKubeletConfigFetcher(conf *config.Configuration, emitter metrics.MetricEmitter) KubeletConfigFetcher {
	nodeAddress := os.Getenv("NODE_ADDRESS")
	if nodeAddress == "" {
		nodeAddress = defaultNodeAddress
		general.Infof("get empty NODE_ADDRESS from env, use 127.0.0.1 by default")
	}

	return &kubeletConfigFetcherImpl{
		emitter:  emitter,
		conf:     conf,
		endpoint: fmt.Sprintf("https://%s:%d%s", nodeAddress, conf.KubeletSecurePort, conf.KubeletConfigURI),
	}
}

// kubeletConfigFetcherImpl use kubelet 10255 pods interface to get pod directly without cache.
type kubeletConfigFetcherImpl struct {
	emitter metrics.MetricEmitter
	conf    *config.Configuration

	endpoint string
}

// GetKubeletConfig gets kubelet config from kubelet 10250/configz api
func (k *kubeletConfigFetcherImpl) GetKubeletConfig(ctx context.Context) (*kubeletconfigv1beta1.KubeletConfiguration, error) {
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
