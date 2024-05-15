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
	"net/http"
	"sync"

	"k8s.io/klog/v2"
)

type AuthType string

const (
	BasicAuth       AuthType = "Basic"
	BearerTokenAuth AuthType = "BearerToken"
)

// ClientAuth holds the HTTP client identity info.
type ClientAuth struct {
	Type        string
	Username    string
	BearerToken string
	Password    string
}

func init() {
	RegisterRoundTripperInitializer(BasicAuth, NewBasicAuthRoundTripperProvider)
	RegisterRoundTripperInitializer(BearerTokenAuth, NewTokenAuthRoundTripperProvider)
}

type RoundTripperFactory interface {
	GetAuthRoundTripper(c *ClientAuth) (http.RoundTripper, error)
}

var roundTripperInitializers sync.Map

// RoundTripperFactoryInitFunc is the function to initialize a authRoundTripper
type RoundTripperFactoryInitFunc func(next http.RoundTripper) (RoundTripperFactory, error)

// RegisterRoundTripperInitializer registers a authRoundTripper initializer function
// for a specific prometheus AuthType, the function will be called when the prometheus client
// is created. The function should return a RoundTripperFactory for the prometheus AuthType. If the
// function is called multiple times for the same AuthType, the last one will be used.
func RegisterRoundTripperInitializer(authType AuthType, initFunc RoundTripperFactoryInitFunc) {
	roundTripperInitializers.Store(authType, initFunc)
}

func GetRegisteredRoundTripperFactoryInitializers() map[AuthType]RoundTripperFactoryInitFunc {
	roundTrippers := make(map[AuthType]RoundTripperFactoryInitFunc)
	roundTripperInitializers.Range(func(key, value interface{}) bool {
		roundTrippers[key.(AuthType)] = value.(RoundTripperFactoryInitFunc)
		return true
	})
	return roundTrippers
}

func GetRoundTripper(c *ClientAuth, next http.RoundTripper,
	initializers map[AuthType]RoundTripperFactoryInitFunc,
) (http.RoundTripper, error) {
	initFunc := initializers[AuthType(c.Type)]
	roundTripperFactory, err := initFunc(next)
	if err != nil {
		klog.ErrorS(err, "get prometheus auth round tripper factory err", "AuthType", c.Type)
		return nil, err
	}
	roundTripper, err := roundTripperFactory.GetAuthRoundTripper(c)
	if err != nil {
		klog.ErrorS(err, "get prometheus auth round tripper err", "AuthType", c.Type)
		return nil, err
	}
	return roundTripper, nil
}
