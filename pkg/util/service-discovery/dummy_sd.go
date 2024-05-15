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

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

const ServiceDiscoveryDummy = "dummy"

func init() { RegisterSDManagerInitializers(ServiceDiscoveryDummy, NewDummyServiceDiscoveryManager) }

type DummyServiceDiscoveryManager struct{}

func NewDummyServiceDiscoveryManager(_ context.Context, _ *katalystbase.GenericContext,
	_ *generic.ServiceDiscoveryConf,
) (ServiceDiscoveryManager, error) {
	return DummyServiceDiscoveryManager{}, nil
}

func (d DummyServiceDiscoveryManager) Name() string                    { return ServiceDiscoveryDummy }
func (d DummyServiceDiscoveryManager) GetEndpoints() ([]string, error) { return []string{}, nil }
func (d DummyServiceDiscoveryManager) Run() error                      { return nil }
