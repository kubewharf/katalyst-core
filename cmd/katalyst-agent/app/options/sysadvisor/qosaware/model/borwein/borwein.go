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

package borwein

import (
	"fmt"

	"github.com/spf13/pflag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/model/borwein"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type BorweinOptions struct {
	InferenceServiceSocketAbsPath      string
	ModelNameToInferenceSvcSockAbsPath map[string]string
	FeatureDescriptionFilePath         string
	NodeFeatureNames                   []string
	ContainerFeatureNames              []string
}

func NewBorweinOptions() *BorweinOptions {
	return &BorweinOptions{
		ModelNameToInferenceSvcSockAbsPath: map[string]string{},
		NodeFeatureNames:                   []string{},
		ContainerFeatureNames:              []string{},
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *BorweinOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.InferenceServiceSocketAbsPath, "borwein-inference-svc-socket-path", o.InferenceServiceSocketAbsPath,
		"socket path which borwein inference server listens at. it's deprecated, use borwein-inference-model-to-svc-socket-path intead")
	fs.StringToStringVar(&o.ModelNameToInferenceSvcSockAbsPath, "borwein-inference-model-to-svc-socket-path", o.ModelNameToInferenceSvcSockAbsPath,
		"model name to socket path which its borwein inference server listens at")
	fs.StringVar(&o.FeatureDescriptionFilePath, "feature-description-filepath", o.FeatureDescriptionFilePath,
		"file path to feature descriptions, the option has lower priority to borwein-node-feature-names and borwein-container-feature-names")
	fs.StringSliceVar(&o.NodeFeatureNames, "borwein-node-feature-names", o.NodeFeatureNames,
		"borwein node feature name list")
	fs.StringSliceVar(&o.ContainerFeatureNames, "borwein-container-feature-names", o.ContainerFeatureNames,
		"borwein node feature name list")
}

// ApplyTo fills up config with options
func (o *BorweinOptions) ApplyTo(c *borwein.BorweinConfiguration) error {
	// todo: currently BorweinParameters are defined statically without options
	FeatureJSONStruct := struct {
		NodeFeatureNames      []string `json:"node_feature_names"`
		ContainerFeatureNames []string `json:"container_feature_names"`
	}{}

	c.InferenceServiceSocketAbsPath = o.InferenceServiceSocketAbsPath
	c.ModelNameToInferenceSvcSockAbsPath = o.ModelNameToInferenceSvcSockAbsPath

	if len(o.NodeFeatureNames)+len(o.ContainerFeatureNames) > 0 {
		c.NodeFeatureNames = o.NodeFeatureNames
		c.ContainerFeatureNames = o.ContainerFeatureNames
	} else if len(o.FeatureDescriptionFilePath) > 0 {
		err := general.LoadJsonConfig(o.FeatureDescriptionFilePath, &FeatureJSONStruct)
		if err != nil {
			return fmt.Errorf("failed to load borwein features, err: %v", err)
		}

		c.NodeFeatureNames = FeatureJSONStruct.NodeFeatureNames
		c.ContainerFeatureNames = FeatureJSONStruct.ContainerFeatureNames
	}

	return nil
}
