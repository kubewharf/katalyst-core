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

package service_discovery

import (
	"context"
	"fmt"
	"sync"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

// ServiceDiscoveryManager is used to discover all available endpoints.
type ServiceDiscoveryManager interface {
	Name() string
	Run() error

	// GetEndpoints get all endpoints list in the format `host:port`,
	// different implementations may have each individual explanations
	// for what the returned value represents for.
	GetEndpoints() ([]string, error)
}

type InitFunc func(ctx context.Context, agentCtx *katalystbase.GenericContext,
	conf *generic.ServiceDiscoveryConf) (ServiceDiscoveryManager, error)

var sdManagerInitializers sync.Map

func RegisterSDManagerInitializers(name string, initFunc InitFunc) {
	sdManagerInitializers.Store(name, initFunc)
}

// GetSDManager return an implementation for ServiceDiscoveryManager based on the given parameters
func GetSDManager(ctx context.Context, agentCtx *katalystbase.GenericContext,
	conf *generic.ServiceDiscoveryConf,
) (ServiceDiscoveryManager, error) {
	name := conf.Name
	if len(name) == 0 {
		name = ServiceDiscoveryPodSinglePort
	}

	val, ok := sdManagerInitializers.Load(name)
	if !ok {
		return DummyServiceDiscoveryManager{}, fmt.Errorf("failed to get sd-manager %v", name)
	}
	f, ok := val.(InitFunc)
	if !ok {
		return DummyServiceDiscoveryManager{}, fmt.Errorf("failed to init sd-manager %v", conf.Name)
	}
	return f(ctx, agentCtx, conf)
}
