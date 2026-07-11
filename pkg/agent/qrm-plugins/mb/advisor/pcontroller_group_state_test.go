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

package advisor

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_slowRecover_Moderate(t *testing.T) {
	t.Parallel()
	type fields struct {
		hitCounts int
	}
	type args struct {
		raises int
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantCap int
		wantOK  bool
	}{
		{
			name: "nok still warm",
			fields: fields{
				hitCounts: 6,
			},
			args: args{
				raises: 1000,
			},
			wantCap: 0,
			wantOK:  false,
		},
		{
			name: "ok if cool",
			fields: fields{
				hitCounts: 30,
			},
			args: args{
				raises: 1000,
			},
			wantCap: 20,
			wantOK:  true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &pipelineRecover{
				recovers: []capRecoveryModerator{
					&coolDownRecover{
						coolCount:         tt.fields.hitCounts,
						coolDownThreshold: 30,
					},
					&reducedRecover{reducerPCT: 2},
				},
			}
			got, got1 := p.Moderate(tt.args.raises)
			assert.Equalf(t, tt.wantCap, got, "Moderate(%v)", tt.args.raises)
			assert.Equalf(t, tt.wantOK, got1, "Moderate(%v)", tt.args.raises)
		})
	}
}
