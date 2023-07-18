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

package process

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

// InsecureConfig returns a kubelet API config object which uses the token path.
func InsecureConfig(host, tokenFile string) (*rest.Config, error) {
	if tokenFile == "" {
		return nil, fmt.Errorf("api auth token file must be defined")
	}
	if len(host) == 0 {
		return nil, fmt.Errorf("kubelet host must be defined")
	}

	token, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, err
	}

	tlsClientConfig := rest.TLSClientConfig{Insecure: true}

	return &rest.Config{
		Host:            host,
		TLSClientConfig: tlsClientConfig,
		BearerToken:     string(token),
		BearerTokenFile: tokenFile,
	}, nil
}

// GetAndUnmarshalForHttps gets data from the given url and unmarshal it into the given struct.
func GetAndUnmarshalForHttps(ctx context.Context, port int, nodeAddress, endpoint, authTokenFile string, v interface{}) error {
	if nodeAddress == "" {
		return fmt.Errorf("get empty NODE_ADDRESS from env")
	}

	u, err := url.ParseRequestURI(fmt.Sprintf("https://%s:%d%s", nodeAddress, port, endpoint))
	if err != nil {
		return fmt.Errorf("failed to parse -kubelet-config-uri: %w", err)
	}

	restConfig, err := InsecureConfig(u.String(), authTokenFile)
	if err != nil {
		return fmt.Errorf("failed to initialize rest config for kubelet config uri: %w", err)
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(restConfig)
	if err != nil {
		return err
	}

	bytes, err := discoveryClient.RESTClient().
		Get().
		Timeout(httpDefaultTimeout).
		Do(ctx).
		Raw()
	if err != nil {
		return err
	}

	if err = json.Unmarshal(bytes, v); err != nil {
		return fmt.Errorf("failed to unmarshal json for kubelet config: %w", err)
	}

	return nil
}
