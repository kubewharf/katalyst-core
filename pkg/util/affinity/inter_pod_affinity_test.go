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

package affinity

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-api/pkg/consts"
)

func TestUnmarshalAffinityAnnotation(t *testing.T) {
	testcases := []struct {
		description string
		annotations map[string]string
		expectResp  *MicroTopologyPodAffnity
		ifErr       bool
	}{
		{
			description: "nil annotations",
			annotations: make(map[string]string),
			expectResp: &MicroTopologyPodAffnity{
				Affinity:     nil,
				AntiAffinity: nil,
			},
			ifErr: false,
		},
		{
			description: "affinity without matchLabel",
			annotations: map[string]string{consts.PodAnnotationMicroTopologyInterPodAffinity: `{"required": [{"zone":"socket"}]}`},
			expectResp:  nil,
			ifErr:       true,
		},
		{
			description: "affinity with matchLabel",
			annotations: map[string]string{consts.PodAnnotationMicroTopologyInterPodAffinity: `{"required": [{"matchLabels": {"k": "v", "k2": "v2"}, "zone":"numa"}]}`},
			expectResp: &MicroTopologyPodAffnity{
				Affinity: &MicroTopologyPodAffinityAnnotation{
					Required: []Selector{
						{
							MatchLabels: map[string]string{"k": "v", "k2": "v2"},
							Zone:        "numa",
						},
					},
				},
				AntiAffinity: nil,
			},
			ifErr: false,
		},
		{
			description: "antiaffinity with matchLabel",
			annotations: map[string]string{consts.PodAnnotationMicroTopologyInterPodAntiAffinity: `{"required": [{"matchLabels": {"k": "v", "k2": "v2"}, "zone":"numa"}]}`},
			expectResp: &MicroTopologyPodAffnity{
				Affinity: nil,
				AntiAffinity: &MicroTopologyPodAffinityAnnotation{
					Required: []Selector{
						{
							MatchLabels: map[string]string{"k": "v", "k2": "v2"},
							Zone:        "numa",
						},
					},
				},
			},
			ifErr: false,
		},
		{
			description: "antiaffinity without zone",
			annotations: map[string]string{consts.PodAnnotationMicroTopologyInterPodAntiAffinity: `{"required": [{"matchLabels": {"k": "v", "k2": "v2"}}]}`},
			expectResp: &MicroTopologyPodAffnity{
				Affinity: nil,
				AntiAffinity: &MicroTopologyPodAffinityAnnotation{
					Required: []Selector{
						{
							MatchLabels: map[string]string{"k": "v", "k2": "v2"},
							Zone:        "numa",
						},
					},
				},
			},
			ifErr: false,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.description, func(t *testing.T) {
			podAffinity, err := UnmarshalAffinity(tc.annotations)
			if tc.ifErr != (err != nil) {
				fmt.Printf("ifErr:%v, err:%v\n", tc.ifErr, err)
			}
			assert.Equal(t, tc.expectResp, podAffinity)
		})
	}
}
