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

package featuregatenegotiation

import (
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

var negotiationTypeFeatureGatesFinder sync.Map

type NegotiationTypeFeatureGatesFinder interface {
	// GetFeatureGate returns the feature gate that needs to be negotiated with sysadvisor from configuration(static environment or dynamic kcc configuration)
	// If the feature gate is nil, it means the feature gate is explicitly disabled
	// If the feature gate is not nil, it means the feature gate is explicitly enabled
	GetFeatureGate(conf *config.Configuration) *advisorsvc.FeatureGate
}

func RegisterNegotiationTypeFeatureGatesFinder(name string, finderFunc NegotiationTypeFeatureGatesFinder) {
	general.Infof("register negotiation feature gate %s", name)
	negotiationTypeFeatureGatesFinder.Store(name, finderFunc)
}

func getNegotiationTypeFeatureGatesFinders() map[string]NegotiationTypeFeatureGatesFinder {
	finders := make(map[string]NegotiationTypeFeatureGatesFinder)
	negotiationTypeFeatureGatesFinder.Range(func(key, value interface{}) bool {
		finders[key.(string)] = value.(NegotiationTypeFeatureGatesFinder)
		return true
	})
	return finders
}

func IsNegotiationTypeFeatureGateSupported(name string) bool {
	_, ok := negotiationTypeFeatureGatesFinder.Load(name)
	return ok
}
