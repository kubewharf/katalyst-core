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
