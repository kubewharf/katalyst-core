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

package decorator

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/spec"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/headroomassembler"
)

func Test_applyDiscount(t *testing.T) {
	t.Parallel()
	type args struct {
		quantity resource.Quantity
		discount float64
	}
	tests := []struct {
		name string
		args args
		want resource.Quantity
	}{
		{
			name: "happy path",
			args: args{
				quantity: resource.MustParse("1"),
				discount: 0.32,
			},
			want: resource.MustParse("320m"),
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := applyDiscount(tt.args.quantity, tt.args.discount); !got.Equal(tt.want) {
				t.Errorf("applyDiscount() = %v, want %v", got, tt.want)
			}
		})
	}
}

type mockInnerAssembler struct {
	mock.Mock
}

func (m *mockInnerAssembler) GetHeadroom() (resource.Quantity, map[int]resource.Quantity, error) {
	args := m.Called()
	return args.Get(0).(resource.Quantity), args.Get(1).(map[int]resource.Quantity), args.Error(2)
}

type mockDiscounter struct {
	mock.Mock
}

func (m *mockDiscounter) GetDiscount() (float64, error) {
	args := m.Called()
	return args.Get(0).(float64), args.Error(1)
}

func Test_discountDecorator_GetHeadroom(t *testing.T) {
	t.Parallel()

	innerMockOK := new(mockInnerAssembler)
	innerMockOK.On("GetHeadroom").Return(
		resource.MustParse("4"),
		map[int]resource.Quantity{
			0: resource.MustParse("1"),
			1: resource.MustParse("3"),
		}, nil,
	)

	discounterMockOK := new(mockDiscounter)
	discounterMockOK.On("GetDiscount").Return(0.48, nil)

	innerMockNG := new(mockInnerAssembler)
	innerMockNG.On("GetHeadroom").Return(
		resource.MustParse("4"),
		map[int]resource.Quantity{
			0: resource.MustParse("1"),
			1: resource.MustParse("3"),
		}, nil,
	)

	discounterMockNG := new(mockDiscounter)
	discounterMockNG.On("GetDiscount").Return(0.0, errors.New("test"))

	type fields struct {
		inner      headroomassembler.HeadroomAssembler
		discounter DiscountGetter
	}
	tests := []struct {
		name    string
		fields  fields
		want    resource.Quantity
		want1   map[int]resource.Quantity
		wantErr bool
	}{
		{
			name: "happy path",
			fields: fields{
				inner:      innerMockOK,
				discounter: discounterMockOK,
			},
			want: resource.MustParse("1920m"),
			want1: map[int]resource.Quantity{
				0: resource.MustParse("480m"),
				1: resource.MustParse("1440m"),
			},
			wantErr: false,
		},
		{
			name: "negative path",
			fields: fields{
				inner:      innerMockNG,
				discounter: discounterMockNG,
			},
			want: resource.MustParse("4"),
			want1: map[int]resource.Quantity{
				0: resource.MustParse("1"),
				1: resource.MustParse("3"),
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := &discountDecorator{
				inner:      tt.fields.inner,
				discounter: tt.fields.discounter,
			}
			got, got1, err := d.GetHeadroom()
			if (err != nil) != tt.wantErr {
				t.Errorf("GetHeadroom() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !got.Equal(tt.want) {
				t.Errorf("GetHeadroom() got = %v, want %v", got, tt.want)
			}
			if !got1[0].Equal(tt.want1[0]) {
				t.Errorf("GetHeadroom() got1[0] = %v, want[0] %v", got1[0], tt.want1[1])
			}
			if !got1[1].Equal(tt.want1[1]) {
				t.Errorf("GetHeadroom() got1[1] = %v, want[1] %v", got1[1], tt.want1[1])
			}

			if tt.fields.inner != nil {
				tt.fields.inner.(*mockInnerAssembler).AssertExpectations(t)
			}
			if tt.fields.discounter != nil {
				tt.fields.discounter.(*mockDiscounter).AssertExpectations(t)
			}
		})
	}
}

type mockSpecFetcher struct {
	mock.Mock
}

func (m *mockSpecFetcher) GetPowerSpec(ctx context.Context) (*spec.PowerSpec, error) {
	args := m.Called(ctx)
	return args.Get(0).(*spec.PowerSpec), args.Error(1)
}

func Test_nodeAnnotationDiscountGetter_GetDiscount(t *testing.T) {
	t.Parallel()

	discounts := map[spec.PowerAlert]float64{
		spec.PowerAlertP1: 0.20,
		spec.PowerAlertP2: 0.40,
		spec.PowerAlertP3: 0.60,
	}

	specFetcherMockP3 := new(mockSpecFetcher)
	specFetcherMockP3.On("GetPowerSpec", context.Background()).Return(
		&spec.PowerSpec{
			Alert: "p3",
		},
		nil,
	)

	specFetcherMockP0 := new(mockSpecFetcher)
	specFetcherMockP0.On("GetPowerSpec", context.Background()).Return(
		&spec.PowerSpec{
			Alert: "p0",
		},
		nil,
	)

	type fields struct {
		specFetcher spec.SpecFetcher
		discounts   map[spec.PowerAlert]float64
	}
	tests := []struct {
		name    string
		fields  fields
		want    float64
		wantErr bool
	}{
		{
			name: "happy path of p3",
			fields: fields{
				specFetcher: specFetcherMockP3,
				discounts:   discounts,
			},
			want:    0.60,
			wantErr: false,
		},
		{
			name: "happy path of p0",
			fields: fields{
				specFetcher: specFetcherMockP0,
				discounts:   discounts,
			},
			want:    0.0,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := &nodeAnnotationDiscountGetter{
				specFetcher: tt.fields.specFetcher,
				discounts:   tt.fields.discounts,
			}
			got, err := d.GetDiscount()
			if (err != nil) != tt.wantErr {
				t.Errorf("GetDiscount() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("GetDiscount() got = %v, want %v", got, tt.want)
			}

			if tt.fields.specFetcher != nil {
				tt.fields.specFetcher.(*mockSpecFetcher).AssertExpectations(t)
			}
		})
	}
}
