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

package qos

import (
	"testing"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
)

func intPtr(x int) *int {
	return &x
}

func TestAlignOOMPriority(t *testing.T) {
	t.Parallel()

	type fields struct {
		qosLevel           string
		userSpecifiedScore *int
	}

	tests := []struct {
		name   string
		fields fields
		want   int
	}{
		{
			name: "reclaimed_cores without user specified score",
			fields: fields{
				qosLevel:           apiconsts.PodAnnotationQoSLevelReclaimedCores,
				userSpecifiedScore: nil,
			},
			want: -100,
		},
		{
			name: "shared_cores without user specified score",
			fields: fields{
				qosLevel:           apiconsts.PodAnnotationQoSLevelSharedCores,
				userSpecifiedScore: nil,
			},
			want: 0,
		},
		{
			name: "dedicated_cores without user specified score",
			fields: fields{
				qosLevel:           apiconsts.PodAnnotationQoSLevelDedicatedCores,
				userSpecifiedScore: nil,
			},
			want: 100,
		},
		{
			name: "system_cores without user specified score",
			fields: fields{
				qosLevel:           apiconsts.PodAnnotationQoSLevelSystemCores,
				userSpecifiedScore: nil,
			},
			want: 200,
		},
		{
			name: "invalid qosLevel without user specified score",
			fields: fields{
				qosLevel:           "",
				userSpecifiedScore: nil,
			},
			want: -100,
		},
		{
			name: "reclaimed_cores with userSpecifiedScore in the correct range",
			fields: fields{
				qosLevel:           apiconsts.PodAnnotationQoSLevelReclaimedCores,
				userSpecifiedScore: intPtr(-50),
			},
			want: -50,
		},
		{
			name: "reclaimed_cores with userSpecifiedScore less than the correct range",
			fields: fields{
				qosLevel:           apiconsts.PodAnnotationQoSLevelReclaimedCores,
				userSpecifiedScore: intPtr(-150),
			},
			want: -100,
		},
		{
			name: "reclaimed_cores with userSpecifiedScore large than the correct range",
			fields: fields{
				qosLevel:           apiconsts.PodAnnotationQoSLevelReclaimedCores,
				userSpecifiedScore: intPtr(100),
			},
			want: -1,
		},
		{
			name: "reclaimed_cores with top userSpecifiedScore",
			fields: fields{
				qosLevel:           apiconsts.PodAnnotationQoSLevelReclaimedCores,
				userSpecifiedScore: intPtr(300),
			},
			want: 300,
		},
		{
			name: "shared_cores with userSpecifiedScore in the correct range",
			fields: fields{
				qosLevel:           apiconsts.PodAnnotationQoSLevelSharedCores,
				userSpecifiedScore: intPtr(39),
			},
			want: 39,
		},
		{
			name: "shared_cores with userSpecifiedScore less than the correct range",
			fields: fields{
				qosLevel:           apiconsts.PodAnnotationQoSLevelSharedCores,
				userSpecifiedScore: intPtr(-100),
			},
			want: 0,
		},
		{
			name: "shared_cores with userSpecifiedScore large than the correct range",
			fields: fields{
				qosLevel:           apiconsts.PodAnnotationQoSLevelSharedCores,
				userSpecifiedScore: intPtr(210),
			},
			want: 99,
		},
		{
			name: "shared_cores with top userSpecifiedScore",
			fields: fields{
				qosLevel:           apiconsts.PodAnnotationQoSLevelSharedCores,
				userSpecifiedScore: intPtr(300),
			},
			want: 300,
		},
		{
			name: "dedicated_cores with userSpecifiedScore in the correct range",
			fields: fields{
				qosLevel:           apiconsts.PodAnnotationQoSLevelDedicatedCores,
				userSpecifiedScore: intPtr(142),
			},
			want: 142,
		},
		{
			name: "dedicated_cores with userSpecifiedScore less than the correct range",
			fields: fields{
				qosLevel:           apiconsts.PodAnnotationQoSLevelDedicatedCores,
				userSpecifiedScore: intPtr(-100),
			},
			want: 100,
		},
		{
			name: "dedicated_cores with userSpecifiedScore large than the correct range",
			fields: fields{
				qosLevel:           apiconsts.PodAnnotationQoSLevelDedicatedCores,
				userSpecifiedScore: intPtr(250),
			},
			want: 199,
		},
		{
			name: "dedicated_cores with top userSpecifiedScore",
			fields: fields{
				qosLevel:           apiconsts.PodAnnotationQoSLevelDedicatedCores,
				userSpecifiedScore: intPtr(300),
			},
			want: 300,
		},
		{
			name: "system_cores with userSpecifiedScore in the correct range",
			fields: fields{
				qosLevel:           apiconsts.PodAnnotationQoSLevelSystemCores,
				userSpecifiedScore: intPtr(221),
			},
			want: 221,
		},
		{
			name: "system_cores with userSpecifiedScore less than the correct range",
			fields: fields{
				qosLevel:           apiconsts.PodAnnotationQoSLevelSystemCores,
				userSpecifiedScore: intPtr(121),
			},
			want: 200,
		},
		{
			name: "system_cores with userSpecifiedScore large than the correct range",
			fields: fields{
				qosLevel:           apiconsts.PodAnnotationQoSLevelSystemCores,
				userSpecifiedScore: intPtr(322),
			},
			want: 299,
		},
		{
			name: "system_cores with top userSpecifiedScore",
			fields: fields{
				qosLevel:           apiconsts.PodAnnotationQoSLevelSystemCores,
				userSpecifiedScore: intPtr(300),
			},
			want: 300,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AlignOOMPriority(tt.fields.qosLevel, tt.fields.userSpecifiedScore); got != tt.want {
				t.Errorf("AlignOOMPriority() = %v, want %v", got, tt.want)
			}
		})
	}
}
