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

package resource_portrait

import (
	"reflect"
	"testing"
	"time"

	"bou.ke/monkey"
	"github.com/h2non/gock"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
)

func TestGeneratePredictionInput(t *testing.T) {
	t.Parallel()

	defer monkey.UnpatchAll()
	now := time.Now()
	monkey.Patch(time.Now, func() time.Time {
		return now
	})

	want := &predictionInput{
		Quantile:            0.99,
		ScaleUpForward:      180,
		Interval:            60,
		PredictionStartTime: time.Now().Unix() / 60 * 60,
		PredictionDuration:  60 * 120,
	}

	type args struct {
		cfg           map[string]string
		historyMetric map[string][]timeSeriesItem
	}
	tests := []struct {
		name    string
		args    args
		want    *predictionInput
		wantErr bool
	}{
		{
			name:    "normal",
			args:    args{},
			want:    want,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := generatePredictionInput(tt.args.cfg, tt.args.historyMetric)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("generatePredictionInput() got=%v, want=%v", got, tt.want)
			}
			if (err != nil) != tt.wantErr {
				t.Errorf("generatePredictionInput() got=%v, want=%v", err, tt.wantErr)
			}
		})
	}
}

func TestPredictionAlgorithmProvider_Predict(t *testing.T) {
	t.Parallel()

	defer gock.Off()

	type fields struct {
		Address  string
		Cfg      map[string]string
		Metrics  map[string][]model.SamplePair
		RespJson string
	}
	type want struct {
		TimeSeries  map[string][]timeSeriesItem
		Periodicity map[string]float64
		err         bool
	}
	tests := []struct {
		name   string
		fields fields
		want   want
	}{
		{
			name: "normal",
			fields: fields{
				Address: "http://localhost:8081",
				Cfg:     map[string]string{},
				Metrics: map[string][]model.SamplePair{
					"CPUUsage": {
						{
							Timestamp: 60,
							Value:     1.2,
						},
					},
					"CPUUsage2": {
						{
							Timestamp: 60,
							Value:     1.2,
						},
					},
				},
				RespJson: `{
    "Code": 200,
    "Msg": "Successful",
    "Periodicity": {
        "CPUUsage": -1,
        "CPUUsage2": -1
    },
    "Reason": "",
    "TimeSeries": {
        "CPUUsage": [
            {
                "Timestamp": 120,
                "Value": 1.2
            }
        ],
        "CPUUsage2": [
            {
                "Timestamp": 120,
                "Value": 1.2
            }
        ]
    }
}`,
			},
			want: want{
				TimeSeries: map[string][]timeSeriesItem{
					"CPUUsage": {
						{
							Timestamp: 120,
							Value:     1.2,
						},
					},
					"CPUUsage2": {
						{
							Timestamp: 120,
							Value:     1.2,
						},
					},
				},
				Periodicity: map[string]float64{
					"periodicity-CPUUsage":  -1,
					"periodicity-CPUUsage2": -1,
				},
			},
		},
		{
			name: "resp unmarshal err",
			fields: fields{
				Address: "http://localhost:8082",
				Cfg:     map[string]string{},
				Metrics: map[string][]model.SamplePair{
					"CPUUsage": {
						{
							Timestamp: 60,
							Value:     1.2,
						},
					},
					"CPUUsage2": {
						{
							Timestamp: 60,
							Value:     1.2,
						},
					},
				},
				RespJson: `{`,
			},
			want: want{
				TimeSeries:  nil,
				Periodicity: nil,
				err:         true,
			},
		},
		{
			name: "resp msg is not successful",
			fields: fields{
				Address: "http://localhost:8083",
				Cfg:     map[string]string{},
				Metrics: map[string][]model.SamplePair{
					"CPUUsage": {
						{
							Timestamp: 60,
							Value:     1.2,
						},
					},
					"CPUUsage2": {
						{
							Timestamp: 60,
							Value:     1.2,
						},
					},
				},
				RespJson: `{
    "Code": 200,
    "Msg": "Failed",
    "Periodicity": {
        "CPUUsage": -1,
        "CPUUsage2": -1
    },
    "Reason": "",
    "TimeSeries": {
        "CPUUsage": [
            {
                "Timestamp": 120,
                "Value": 1.2
            }
        ],
        "CPUUsage2": [
            {
                "Timestamp": 120,
                "Value": 1.2
            }
        ]
    }
}`,
			},
			want: want{
				TimeSeries:  nil,
				Periodicity: nil,
				err:         true,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// http resp mock
			gock.New(tt.fields.Address).
				Post("/openapi/tsa/predict").
				Reply(200).
				JSON(tt.fields.RespJson)

			p, _ := NewAlgorithmProvider(tt.fields.Address, ResourcePortraitMethodPredict)
			p.SetConfig(tt.fields.Cfg)
			p.SetMetrics(tt.fields.Metrics)
			timeseries, data, err := p.Call()
			if (err != nil) != tt.want.err {
				t.Errorf("Predict Algorithm Provider Call() error = %v, wantErr %v", err, tt.want.err)
			}
			if !reflect.DeepEqual(timeseries, tt.want.TimeSeries) {
				t.Errorf("Predict Algorithm Provider Call() got=%v, want=%v", timeseries, tt.want.TimeSeries)
			}
			if !reflect.DeepEqual(data, tt.want.Periodicity) {
				t.Errorf("Predict Algorithm Provider Call() got=%v, want=%v", data, tt.want.Periodicity)
			}
		})
	}
}

func TestMethod(t *testing.T) {
	t.Parallel()
	t.Run("normal", func(t *testing.T) {
		t.Parallel()

		p, _ := NewAlgorithmProvider("test.com", ResourcePortraitMethodPredict)
		assert.Equal(t, p.Method(), ResourcePortraitMethodPredict)
	})
}

func TestNewAlgorithmProvider(t *testing.T) {
	t.Parallel()
	t.Run("normal", func(t *testing.T) {
		t.Parallel()

		p, err := NewAlgorithmProvider("test.com", ResourcePortraitMethodPredict)
		assert.Nil(t, err)
		assert.NotNil(t, p)
	})
	t.Run("error", func(t *testing.T) {
		t.Parallel()

		p, err := NewAlgorithmProvider("test.com", "predictx")
		assert.NotNil(t, err)
		assert.Nil(t, p)
	})
}
