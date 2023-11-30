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

package metric

import (
	"time"

	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

type StoreConfiguration struct {
	StoreName      string
	GCPeriod       time.Duration
	PurgePeriod    time.Duration
	IndexLabelKeys []string

	StoreServerShardCount   int
	StoreServerReplicaTotal int

	*generic.ServiceDiscoveryConf
}

func NewStoreConfiguration() *StoreConfiguration {
	return &StoreConfiguration{
		GCPeriod:             time.Second * 10,
		PurgePeriod:          time.Second * 600,
		ServiceDiscoveryConf: generic.NewServiceDiscoveryConf(),
	}
}
