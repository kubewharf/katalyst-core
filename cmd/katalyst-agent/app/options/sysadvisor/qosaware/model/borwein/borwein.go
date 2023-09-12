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
	"github.com/spf13/pflag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/model/borwein"
)

type BorweinOptions struct {
	InferenceServiceSocketAbsPath string
}

func NewBorweinOptions() *BorweinOptions {
	return &BorweinOptions{}
}

// AddFlags adds flags to the specified FlagSet.
func (o *BorweinOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.InferenceServiceSocketAbsPath, "borwein-inference-svc-socket-path", o.InferenceServiceSocketAbsPath,
		"socket path which borwein inference server listens at")
}

// ApplyTo fills up config with options
func (o *BorweinOptions) ApplyTo(c *borwein.BorweinConfiguration) error {
	// todo: currently BorweinParameters, NodeFeatureNames, ContainerFeatureNames are defined statically without options
	c.InferenceServiceSocketAbsPath = o.InferenceServiceSocketAbsPath
	return nil
}
