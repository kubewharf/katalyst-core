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

package native

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGenerateURI(t *testing.T) {
	t.Parallel()

	port := 10250
	nodeAddress := "127.0.0.1"
	endpoint := "/configz"

	// ipv4
	uri, err := generateURI(port, nodeAddress, endpoint)
	assert.NoError(t, err)
	assert.Equal(t, "https://127.0.0.1:10250/configz", uri)

	// ipv6
	nodeAddress = "::1"
	uri, err = generateURI(port, nodeAddress, endpoint)
	assert.NoError(t, err)
	assert.Equal(t, "https://[::1]:10250/configz", uri)
}

func TestInsecureConfig(t *testing.T) {
	t.Parallel()

	host := "https://127.0.0.1:10250/configz"
	authTokenFile := "/var/run/secrets/kubernetes.io/serviceaccount/token"

	_, err := insecureConfig(host, authTokenFile)
	assert.Error(t, err)
}

func TestGetAndUnmarshalForHttps(t *testing.T) {
	t.Parallel()

	port := 10250
	nodeAddress := "127.0.0.1"
	endpoint := "/configz"
	authTokenFile := "/var/run/secrets/kubernetes.io/serviceaccount/token"

	type configzWrapper struct {
		ComponentConfig KubeletConfiguration `json:"kubeletconfig"`
	}
	configz := configzWrapper{}

	err := GetAndUnmarshalForHttps(context.TODO(), port, nodeAddress, endpoint, authTokenFile, &configz)
	assert.Error(t, err)
}
