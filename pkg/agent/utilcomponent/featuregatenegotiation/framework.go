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
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation/finders"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type FeatureGateManager interface {
	GetWantedFeatureGates(featureGateType string) (map[string]*advisorsvc.FeatureGate, error)
}

func NewFeatureGateManager(conf *config.Configuration) FeatureGateManager {
	return &featureGateManagerImpl{
		conf: conf,
	}
}

type featureGateManagerImpl struct {
	conf *config.Configuration
}

func (m *featureGateManagerImpl) GetWantedFeatureGates(featureGateType string) (map[string]*advisorsvc.FeatureGate, error) {
	featureGates, err := GenerateNegotiationFeatureGates(m.conf, featureGateType)
	if err != nil {
		return nil, fmt.Errorf("GenerateNegotiationFeatureGates failed with error: %v", err)
	}
	return featureGates, nil
}

// GenerateNegotiationFeatureGates retrieves all the specific type feature gates that need to be negotiated with sysadvisor from the static configuration
func GenerateNegotiationFeatureGates(conf *config.Configuration, featureGateType string) (map[string]*advisorsvc.FeatureGate, error) {
	return getFeatureGates(func(finder NegotiationTypeFeatureGatesFinder) *advisorsvc.FeatureGate {
		return finder.GetFeatureGate(conf)
	}, featureGateType)
}

func getFeatureGates(getFeatureGate func(NegotiationTypeFeatureGatesFinder) *advisorsvc.FeatureGate, featureGateType string) (map[string]*advisorsvc.FeatureGate, error) {
	featureGatesFinders := getNegotiationTypeFeatureGatesFinders()
	featureGates := make(map[string]*advisorsvc.FeatureGate)

	if !finders.SupportedFeatureGateTypes.Has(featureGateType) {
		return nil, fmt.Errorf("unknown featureGate type %v", featureGateType)
	}

	for name, finder := range featureGatesFinders {
		f := getFeatureGate(finder)
		if f == nil {
			general.Infof("negotiation feature gate %v is disabled", name)
			continue
		}
		if f.Type != featureGateType {
			continue
		}
		if _, exist := featureGates[f.Name]; exist {
			return nil, fmt.Errorf("duplicate negotiation feature gate %v", f)
		}
		featureGates[name] = f
	}

	// validation
	for name, featureGate := range featureGates {
		if featureGate == nil {
			return nil, fmt.Errorf("featureGate %v is nil", name)
		}
		if !IsNegotiationTypeFeatureGateSupported(name) {
			return nil, fmt.Errorf("negotiation feature gate %s is unknown", name)
		}
		if name != featureGate.Name {
			return nil, fmt.Errorf("negotiation feature gate name %s is not equal to feature gate %v", name, featureGate)
		}
		if !finders.SupportedFeatureGateTypes.Has(featureGate.Type) {
			return nil, fmt.Errorf("negotiation feature gate type %s is unknown", featureGate.Type)
		}
	}
	return featureGates, nil
}

// GetWantedButNotSupportedFeatureGates filters out feature gates in 'wantedFeatureGates' that are not present in 'supportedFeatureGates'.
func GetWantedButNotSupportedFeatureGates(wantedFeatureGates map[string]*advisorsvc.FeatureGate, supportedFeatureGates map[string]*advisorsvc.FeatureGate) map[string]*advisorsvc.FeatureGate {
	result := make(map[string]*advisorsvc.FeatureGate)
	for key, value := range wantedFeatureGates {
		_, exists := supportedFeatureGates[key]
		if !exists {
			result[key] = value
		}
	}
	return result
}

func GenerateSupportedWantedFeatureGates(wantedFeatureGates map[string]*advisorsvc.FeatureGate, featureGateType string) (map[string]*advisorsvc.FeatureGate, error) {
	// check negotiation feature gate type
	for name, featureGate := range wantedFeatureGates {
		if featureGate == nil {
			return nil, fmt.Errorf("%v type negotiation feature gate %v is nil", featureGateType, name)
		}
		if featureGate.Type != featureGateType {
			general.Errorf("%v qosware server received unexpected feature gate %v", featureGateType, name)
			return nil, fmt.Errorf("unexpected feature gate %v", name)
		}
	}
	// if any feature gate that requires mutually supported but is not supported, return error
	supportedWantedFeatureGates, notSupportedFeatureGates := splitFeatureGatesBySupport(wantedFeatureGates)
	for name, featureGate := range notSupportedFeatureGates {
		if featureGate.MustMutuallySupported {
			general.Errorf("%v type feature gate %v is not supported by sysadvisor", featureGateType, name)
			return nil, fmt.Errorf("%v type feature gate %v is not supported by sysadvisor", featureGateType, name)
		} else {
			general.Warningf("%v type feature gate %v is not supported by sysadvisor, but is compatible", featureGateType, name)
		}
	}
	return supportedWantedFeatureGates, nil
}

// SplitFeatureGatesBySupport separates wanted feature gates into supported and unsupported ones
// It takes a map of wanted feature gates and returns two maps: one for supported and one for unsupported feature gates
func splitFeatureGatesBySupport(wantedFeatureGates map[string]*advisorsvc.FeatureGate) (map[string]*advisorsvc.FeatureGate, map[string]*advisorsvc.FeatureGate) {
	wantedSupportedFeatureGates := make(map[string]*advisorsvc.FeatureGate)
	notSupportedFeatureGates := make(map[string]*advisorsvc.FeatureGate)
	for name, featureGate := range wantedFeatureGates {
		if !IsNegotiationTypeFeatureGateSupported(name) {
			notSupportedFeatureGates[name] = featureGate
			continue
		}
		wantedSupportedFeatureGates[name] = featureGate
	}
	return wantedSupportedFeatureGates, notSupportedFeatureGates
}

type FeatureGatesNotSupportedError struct {
	WantedButNotSupportedFeatureGates map[string]*advisorsvc.FeatureGate
}

func (e FeatureGatesNotSupportedError) Error() string {
	return fmt.Sprintf("QRM wanted but sysadvisor not supported feature gates: %v", e.WantedButNotSupportedFeatureGates)
}

func IsFeatureGatesNotSupportedError(err error) bool {
	if err == nil {
		return false
	}
	_, ok := err.(FeatureGatesNotSupportedError)
	return ok
}
