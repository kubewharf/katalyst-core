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

package podadmit

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPodGrouper_GetQoSGroup(t *testing.T) {
	t.Parallel()
	type fields struct {
		poolToSharedSubgroup  map[string]int
		defaultSharedSubgroup int
	}
	type args struct {
		qosLevel    string
		annotations map[string]string
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    string
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "shared_cores batch is shared-30",
			fields: fields{
				poolToSharedSubgroup: map[string]int{
					"batch": 30,
				},
			},
			args: args{
				qosLevel: "shared_cores",
				annotations: map[string]string{
					"cpuset_pool": "batch",
				},
			},
			want:    "shared-30",
			wantErr: assert.NoError,
		},
		{
			name: "shared_cores default is shared-50",
			fields: fields{
				poolToSharedSubgroup: map[string]int{
					"batch": 30,
				},
				defaultSharedSubgroup: 50,
			},
			args: args{
				qosLevel: "shared_cores",
			},
			want:    "shared-50",
			wantErr: assert.NoError,
		},
		{
			name:   "dedicated_cored is dedicated",
			fields: fields{},
			args: args{
				qosLevel: "dedicated_cores",
			},
			want:    "dedicated",
			wantErr: assert.NoError,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := PodGrouper{
				poolToSharedSubgroup:  tt.fields.poolToSharedSubgroup,
				defaultSharedSubgroup: tt.fields.defaultSharedSubgroup,
			}
			got, err := p.GetQoSGroup(tt.args.qosLevel, tt.args.annotations)
			if !tt.wantErr(t, err, fmt.Sprintf("GetQoSGroup(%v, %v)", tt.args.qosLevel, tt.args.annotations)) {
				return
			}
			assert.Equalf(t, tt.want, got, "GetQoSGroup(%v, %v)", tt.args.qosLevel, tt.args.annotations)
		})
	}
}
