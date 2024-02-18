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

package auth

import (
	"fmt"
	"net/http"

	"github.com/prometheus/common/config"
	"k8s.io/klog/v2"
)

type basicAuthRoundTripperProvider struct {
	next http.RoundTripper
}

func NewBasicAuthRoundTripperProvider(next http.RoundTripper) (RoundTripperFactory, error) {
	return &basicAuthRoundTripperProvider{
		next: next,
	}, nil
}

func (p *basicAuthRoundTripperProvider) GetAuthRoundTripper(c *ClientAuth) (http.RoundTripper, error) {
	klog.Info("prom auth using basic auth")
	if len(c.Username) == 0 || len(c.Password) == 0 {
		return nil, fmt.Errorf("invalid user name or password for basic auth")
	}
	return config.NewBasicAuthRoundTripper(c.Username, config.Secret(c.Password), "", p.next), nil
}
