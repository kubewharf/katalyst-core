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

package dynamicpolicy

import (
	"encoding/json"
	"io/ioutil"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
)

type ExtraControlKnobConfigs map[string]ExtraControlKnobConfig

type ExtraControlKnobConfig struct {
	PodExplicitlyAnnotationKey string            `json:"pod_explicitly_annotation_key"`
	QoSLevelToDefaultValue     map[string]string `json:"qos_level_to_default_value"`
	state.ControlKnobInfo      `json:"control_knob_info"`
}

func loadExtraControlKnobConfigs(extraControlKnobConfigAbsPath string) (ExtraControlKnobConfigs, error) {
	configBytes, err := ioutil.ReadFile(extraControlKnobConfigAbsPath)
	if err != nil {
		return nil, err
	}

	extraControlKnobConfigs := make(ExtraControlKnobConfigs)

	err = json.Unmarshal(configBytes, &extraControlKnobConfigs)
	if err != nil {
		return nil, err
	}

	return extraControlKnobConfigs, nil
}
