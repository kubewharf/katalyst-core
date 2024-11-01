package podadmit

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPodGrouper_GetQoSGroup(t *testing.T) {
	t.Parallel()
	type fields struct {
		poolToSharedSubgroup  map[string]string
		defaultSharedSubgroup string
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
				poolToSharedSubgroup: map[string]string{
					"batch": "shared-30",
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
				poolToSharedSubgroup: map[string]string{
					"batch": "shared-30",
				},
				defaultSharedSubgroup: "shared-50",
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
