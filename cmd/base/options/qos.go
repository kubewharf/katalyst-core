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

package options

import (
	"fmt"
	"strings"

	"github.com/spf13/pflag"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

type QoSOptions struct {
	PodAnnotationQoSLevelReclaimedSelector []string
	PodAnnotationQoSLevelSharedSelector    []string
	PodAnnotationQoSLevelDedicatedSelector []string
	PodAnnotationQoSLevelSystemSelector    []string

	PodAnnotationQoSEnhancements []string
	EnhancementDefaultValues     map[string]string
	NUMAInterPodAffinityLabels   []string
}

func NewQoSOptions() *QoSOptions {
	return &QoSOptions{
		EnhancementDefaultValues: make(map[string]string),
	}
}

// AddFlags adds flags  to the specified FlagSet.
func (o *QoSOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringSliceVar(&o.PodAnnotationQoSLevelReclaimedSelector, "pod-annotation-qos-level-reclaimed-selector",
		o.PodAnnotationQoSLevelReclaimedSelector, "selector of qos level pod annotation which show it is reclaimed pod")
	fs.StringSliceVar(&o.PodAnnotationQoSLevelSharedSelector, "pod-annotation-qos-level-shared-selector",
		o.PodAnnotationQoSLevelSharedSelector, "selector of qos level pod annotation which show it is shared pod")
	fs.StringSliceVar(&o.PodAnnotationQoSLevelDedicatedSelector, "pod-annotation-qos-level-dedicated-selector",
		o.PodAnnotationQoSLevelDedicatedSelector, "selector of qos level pod annotation which show it is dedicated pod")
	fs.StringSliceVar(&o.PodAnnotationQoSLevelSystemSelector, "pod-annotation-qos-level-system-selector",
		o.PodAnnotationQoSLevelSystemSelector, "selector of qos level pod annotation which show it is system pod")

	fs.StringSliceVar(&o.PodAnnotationQoSEnhancements, "pod-annotation-qos-enhancements",
		o.PodAnnotationQoSEnhancements, "qos enhancement mappers for katalyst")
	fs.StringToStringVar(&o.EnhancementDefaultValues, "qos-enhancement-default-values",
		o.EnhancementDefaultValues, "qos enhancement default values for corresponding keys")
	fs.StringSliceVar(&o.NUMAInterPodAffinityLabels, "qos-inter-pod-affinity-labels",
		o.NUMAInterPodAffinityLabels, "qos numa-level inter-pod affinity labels")
}

func (o *QoSOptions) ApplyTo(c *generic.QoSConfiguration) error {
	qosLevelSelectorMap := map[string][]string{
		apiconsts.PodAnnotationQoSLevelReclaimedCores: o.PodAnnotationQoSLevelReclaimedSelector,
		apiconsts.PodAnnotationQoSLevelSharedCores:    o.PodAnnotationQoSLevelSharedSelector,
		apiconsts.PodAnnotationQoSLevelDedicatedCores: o.PodAnnotationQoSLevelDedicatedSelector,
		apiconsts.PodAnnotationQoSLevelSystemCores:    o.PodAnnotationQoSLevelSystemSelector,
	}
	for qosLevel, podAnnotationQoSLevelSelector := range qosLevelSelectorMap {
		if err := o.applyToExpandQoSLevel(c, qosLevel, podAnnotationQoSLevelSelector); err != nil {
			return err
		}
	}

	if err := o.applyToExpandQoSEnhancement(c, o.PodAnnotationQoSEnhancements); err != nil {
		return err
	}

	o.applyToEnhancementDefaultValues(c, o.EnhancementDefaultValues)

	o.applyToNUMAInterPodAffinityLabels(c, o.NUMAInterPodAffinityLabels)

	return nil
}

func (o *QoSOptions) applyToExpandQoSLevel(c *generic.QoSConfiguration, qosLevel string, podAnnotationQoSLevelSelector []string) error {
	selectorMap := make(map[string]string)
	for _, s := range podAnnotationQoSLevelSelector {
		sList := strings.Split(strings.TrimSpace(s), "=")
		if len(sList) != 2 {
			return fmt.Errorf("qosLevel %v with invalid selector: %v", qosLevel, s)
		}

		selectorMap[strings.TrimSpace(sList[0])] = strings.TrimSpace(sList[1])
	}

	c.SetExpandQoSLevelSelector(qosLevel, selectorMap)
	return nil
}

func (o *QoSOptions) applyToExpandQoSEnhancement(c *generic.QoSConfiguration, podAnnotationQoSEnhancements []string) error {
	enhancementMap := make(map[string]string)
	for _, s := range podAnnotationQoSEnhancements {
		sList := strings.Split(strings.TrimSpace(s), "=")
		if len(sList) != 2 {
			return fmt.Errorf("qosEnhancement with invalid value: %v", s)
		}

		enhancementMap[strings.TrimSpace(sList[0])] = strings.TrimSpace(sList[1])
	}

	c.SetExpandQoSEnhancementSelector(enhancementMap)
	return nil
}

func (o *QoSOptions) applyToEnhancementDefaultValues(c *generic.QoSConfiguration, enhancementDefaultValues map[string]string) {
	c.SetEnhancementDefaultValues(enhancementDefaultValues)
}

func (o *QoSOptions) applyToNUMAInterPodAffinityLabels(c *generic.QoSConfiguration, NUMAInterPodAffinityLabels []string) error {
	labelsMap := make(map[string]string)
	for _, s := range NUMAInterPodAffinityLabels {
		sList := strings.Split(strings.TrimSpace(s), "=")
		if len(sList) != 2 {
			return fmt.Errorf("qosEnhancement with invalid value: %v", s)
		}

		labelsMap[strings.TrimSpace(sList[0])] = strings.TrimSpace(sList[1])
	}

	c.SetNUMAInterPodAffinityLabelsSelector(labelsMap)
	return nil
}
