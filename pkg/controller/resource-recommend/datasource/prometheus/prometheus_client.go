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

package prometheus

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"time"

	prometheusapi "github.com/prometheus/client_golang/api"

	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/datasource/prometheus/auth"
)

// PromConfig represents the config of prometheus
type PromConfig struct {
	Address            string
	Timeout            time.Duration
	KeepAlive          time.Duration
	InsecureSkipVerify bool
	Auth               auth.ClientAuth

	QueryConcurrency            int
	BRateLimit                  bool
	MaxPointsLimitPerTimeSeries int
	TLSHandshakeTimeoutInSecond time.Duration

	BaseFilter string
}

type prometheusAuthClient struct {
	client prometheusapi.Client
}

// URL implements prometheus client interface
func (p *prometheusAuthClient) URL(ep string, args map[string]string) *url.URL {
	return p.client.URL(ep, args)
}

// Do implements prometheus client interface, wrapped with an auth info
func (p *prometheusAuthClient) Do(ctx context.Context, request *http.Request) (*http.Response, []byte, error) {
	return p.client.Do(ctx, request)
}

// NewPrometheusClient returns a prometheus.Client
func NewPrometheusClient(config *PromConfig) (prometheusapi.Client, error) {
	tlsConfig := &tls.Config{InsecureSkipVerify: config.InsecureSkipVerify}

	var rt http.RoundTripper = &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   config.Timeout,
			KeepAlive: config.KeepAlive,
		}).DialContext,
		TLSHandshakeTimeout: config.TLSHandshakeTimeoutInSecond,
		TLSClientConfig:     tlsConfig,
	}

	t, err := auth.GetRoundTripper(&config.Auth, rt, auth.GetRegisteredRoundTripperFactoryInitializers())
	if err != nil {
		return nil, err
	}

	pc := prometheusapi.Config{
		Address:      config.Address,
		RoundTripper: t,
	}
	return newPrometheusAuthClient(pc)
}

func newPrometheusAuthClient(config prometheusapi.Config) (prometheusapi.Client, error) {
	c, err := prometheusapi.NewClient(config)
	if err != nil {
		return nil, err
	}

	client := &prometheusAuthClient{
		client: c,
	}

	return client, nil
}
