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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/common/model"
	"github.com/samber/lo"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// timeSeriesItem defines the time series format for accessing external algorithm modules.
type timeSeriesItem struct {
	Timestamp int64   `json:"Timestamp"`
	Value     float64 `json:"Value"`
}

type AlgorithmProvider interface {
	Method() string
	SetConfig(cfg map[string]string)
	SetMetrics(metrics map[string][]model.SamplePair)

	// Call is used to call an external algorithm provider and return algorithm analysis data.
	// The first return value represents time series data, the second return value is used to
	// represent unstructured data, recorded in a form similar to tag=float/int/enum.
	Call() (map[string][]timeSeriesItem, map[string]float64, error)
}

var registry = map[string]func(string) AlgorithmProvider{}

func register(algorithmType string, f func(string) AlgorithmProvider) {
	registry[algorithmType] = f
}

func NewAlgorithmProvider(addr, algorithmType string) (AlgorithmProvider, error) {
	if _, ok := registry[algorithmType]; !ok {
		return nil, fmt.Errorf("not supported algorithmType: %s", algorithmType)
	}
	return registry[algorithmType](addr), nil
}

func init() {
	register(ResourcePortraitMethodPredict, newPredictionProvider)
}

// predictProviderImpl is used to call the time series prediction algorithm based on the given
// algorithm configuration and historical data and return the prediction data and periodicity.
type predictProviderImpl struct {
	Address string
	Cfg     map[string]string
	Metrics map[string][]timeSeriesItem
}

type predictionInput struct {
	WorkloadName        string                      `json:"WorkloadName"`
	WorkloadNamespace   string                      `json:"WorkloadNamespace"`
	WorkloadType        string                      `json:"WorkloadType"`
	ServingType         string                      `json:"ServingType"`
	Quantile            float64                     `json:"Quantile"`
	ScaleUpForward      int32                       `json:"ScaleUpForward"`
	Interval            int32                       `json:"Interval"`
	PredictionStartTime int64                       `json:"PredictionStartTime"`
	PredictionDuration  int64                       `json:"PredictionDuration"`
	TimeSeries          map[string][]timeSeriesItem `json:"TimeSeries"`
}

type predictionOutput struct {
	Code        int32                       `json:"Code"`
	Msg         string                      `json:"Msg,omitempty"`
	Reason      string                      `json:"Reason,omitempty"`
	TimeSeries  map[string][]timeSeriesItem `json:"TimeSeries"`
	Periodicity map[string]float64          `json:"Periodicity"`
}

const (
	algorithmMsgSuccessful  = "Successful"
	algorithmServingTimeout = 120

	ResourcePortraitMethodPredict         = "predict"
	resourcePortraitRequestPathPrediction = "/openapi/tsa/predict"

	// periodicityMetricsPrefix is used to mark whether the current indicator is a
	// predicted indicator value or the periodicity of historical data indicators.
	// Periodicity is also a numerical value.
	periodicityMetricsPrefix = "periodicity-"
)

// default algorithm params
const (
	defaultPredictionInputKeyQuantile   = "quantile"
	defaultPredictionInputValueQuantile = "0.99"

	defaultPredictionInputKeyScaleUpForward   = "scaleUpForward"
	defaultPredictionInputValueScaleUpForward = "180"

	defaultPredictionInputKeyDuration   = "duration"
	defaultPredictionInputValueDuration = "1m"

	defaultPredictionInputKeySteps   = "step"
	defaultPredictionInputValueSteps = "120"
)

var defaultPredictionInputMap = map[string]string{
	defaultPredictionInputKeyQuantile:       defaultPredictionInputValueQuantile,
	defaultPredictionInputKeyScaleUpForward: defaultPredictionInputValueScaleUpForward,
	defaultPredictionInputKeyDuration:       defaultPredictionInputValueDuration,
	defaultPredictionInputKeySteps:          defaultPredictionInputValueSteps,
}

func newPredictionProvider(addr string) AlgorithmProvider {
	return &predictProviderImpl{Address: addr}
}

func (p *predictProviderImpl) Method() string {
	return ResourcePortraitMethodPredict
}

func (p *predictProviderImpl) SetConfig(cfg map[string]string) {
	p.Cfg = cfg
}

func (p *predictProviderImpl) SetMetrics(metrics map[string][]model.SamplePair) {
	p.Metrics = map[string][]timeSeriesItem{}
	for k, v := range metrics {
		var items []timeSeriesItem
		for _, item := range v {
			items = append(items, timeSeriesItem{
				// from milliseconds to second
				Timestamp: int64(item.Timestamp) / 1000,
				Value:     float64(item.Value),
			})
		}
		p.Metrics[k] = items
	}
}

func (p *predictProviderImpl) Call() (map[string][]timeSeriesItem, map[string]float64, error) {
	br, err := generateBodyByteReader(p.Cfg, p.Metrics, generatePredictionInput)
	if err != nil {
		return nil, nil, err
	}

	address := p.Address + resourcePortraitRequestPathPrediction
	req, err := http.NewRequest("POST", address, br)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := http.Client{Timeout: algorithmServingTimeout * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer req.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("predict algorithm got httpcode=%d, body=%s", resp.StatusCode, string(body))
	}

	output := &predictionOutput{}
	err = json.Unmarshal(body, output)
	if err != nil {
		return nil, nil, err
	} else if output.Msg != algorithmMsgSuccessful {
		return nil, nil, fmt.Errorf("predict algorithm got unexpected resp msg, msg=%s, reason=%s", output.Msg, output.Reason)
	}

	output.Periodicity = lo.MapKeys(output.Periodicity, func(_ float64, key string) string {
		return periodicityMetricsPrefix + key
	})
	return output.TimeSeries, output.Periodicity, err
}

func generatePredictionInput(cfg map[string]string, historyMetric map[string][]timeSeriesItem) (interface{}, error) {
	cfg = general.MergeMap(defaultPredictionInputMap, cfg)
	duration, err := time.ParseDuration(cfg[defaultPredictionInputKeyDuration])
	if err != nil {
		return nil, err
	}

	steps, err := strconv.Atoi(cfg[defaultPredictionInputKeySteps])
	if err != nil {
		return nil, err
	}

	// Align the start time of predicted data to whole minutes.
	PredictionStartTime := time.Now().Unix() / 60 * 60
	interval := duration.Seconds()
	PredictionDuration := interval * float64(steps)

	quantile, err := strconv.ParseFloat(cfg[defaultPredictionInputKeyQuantile], 64)
	if err != nil {
		return nil, err
	}
	scaleUpForward, err := strconv.ParseInt(cfg[defaultPredictionInputKeyScaleUpForward], 10, 32)
	if err != nil {
		return nil, err
	}

	return &predictionInput{
		Quantile:            quantile,
		ScaleUpForward:      int32(scaleUpForward),
		Interval:            int32(interval),
		PredictionStartTime: PredictionStartTime,
		PredictionDuration:  int64(PredictionDuration),
		TimeSeries:          historyMetric,
	}, nil
}

// generateBodyByteReader is used to generate the body of http request
func generateBodyByteReader(cfg map[string]string, historyMetric map[string][]timeSeriesItem, convertFunc func(map[string]string, map[string][]timeSeriesItem) (interface{}, error)) (*bytes.Reader, error) {
	bbr, err := convertFunc(cfg, historyMetric)
	if bbr == nil {
		return nil, fmt.Errorf("convertFunc err: %v", err)
	}

	bs, err := json.Marshal(bbr)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(bs), nil
}
