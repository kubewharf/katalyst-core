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

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
)

type CollectorConfiguration struct {
	// PodSelector and NodeSelector are used to only match with those resources that real-time pipeline needed
	PodSelector  labels.Selector
	NodeSelector labels.Selector
	SyncInterval time.Duration

	// ShardNum is used to indicate which shard splits current collector will be responsible for
	// todo: currently, we don't support to ShardNum to be set > 1
	ShardNum int

	// CollectorName is used to switch from different collector implementations.
	CollectorName string

	// CredentialPath is the path where the credential files should be in. Which and how many files should be in it
	// depends on the authentication method. For now, we only support basic auth,so there should be two files with name
	// username and password.
	CredentialPath string

	// metricFilter indicates which metrics will be collected.If metricFilter is not set,all metrics will be collected.
	MetricFilter sets.String
}

func NewCollectorConfiguration() *CollectorConfiguration {
	return &CollectorConfiguration{}
}
