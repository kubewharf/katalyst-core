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

// Code generated by protoc-gen-go. DO NOT EDIT.
package inferencesvc

import (
	context "context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInferencePB(t *testing.T) {
	t.Parallel()

	req := &InferenceRequest{
		FeatureNames: []string{"test1", "test2"},
		PodRequestEntries: map[string]*ContainerRequestEntries{
			"pod1": {
				ContainerFeatureValues: map[string]*FeatureValues{
					"container1": {
						Values: []string{"1", "2"},
					},
				},
			},
		},
	}
	_ = req.String()
	req.ProtoMessage()
	req.XXX_Size()
	req.XXX_Merge(req)
	req.XXX_Unmarshal(nil)
	req.XXX_DiscardUnknown()
	req.GetPodRequestEntries()
	req.GetFeatureNames()
	bytes, err := req.Marshal()
	require.NoError(t, err)
	err = req.Unmarshal(bytes)
	require.NoError(t, err)
	req.Reset()

	resp := &InferenceResponse{
		PodResponseEntries: map[string]*ContainerResponseEntries{
			"pod1": {
				ContainerInferenceResults: map[string]*InferenceResults{
					"container1": &InferenceResults{
						InferenceResults: []*InferenceResult{
							{
								IsDefault:     false,
								InferenceType: InferenceType_ClassificationOverload,
								Output:        55,
								Percentile:    60,
							},
							{
								IsDefault:     false,
								InferenceType: InferenceType_LatencyRegression,
								Output:        55,
								Percentile:    60,
							},
						},
					},
				},
			},
		},
	}
	_ = resp.String()
	resp.ProtoMessage()
	resp.XXX_Size()
	resp.XXX_Merge(resp)
	resp.XXX_Unmarshal(nil)
	resp.XXX_DiscardUnknown()
	resp.GetPodResponseEntries()
	bytes, err = resp.Marshal()
	require.NoError(t, err)
	err = resp.Unmarshal(bytes)
	require.NoError(t, err)
	resp.Reset()

	containerRequestEntries := &ContainerRequestEntries{
		ContainerFeatureValues: map[string]*FeatureValues{
			"container1": {
				Values: []string{"1", "2"},
			},
		},
	}
	_ = containerRequestEntries.String()
	containerRequestEntries.ProtoMessage()
	containerRequestEntries.XXX_Size()
	containerRequestEntries.XXX_Merge(containerRequestEntries)
	containerRequestEntries.XXX_Unmarshal(nil)
	containerRequestEntries.XXX_DiscardUnknown()
	containerRequestEntries.GetContainerFeatureValues()
	bytes, err = containerRequestEntries.Marshal()
	require.NoError(t, err)
	err = containerRequestEntries.Unmarshal(bytes)
	require.NoError(t, err)
	containerRequestEntries.Reset()

	featureValues := &FeatureValues{
		Values: []string{"1", "2"},
	}
	_ = featureValues.String()
	featureValues.ProtoMessage()
	featureValues.XXX_Size()
	featureValues.XXX_Merge(featureValues)
	featureValues.XXX_Unmarshal(nil)
	featureValues.XXX_DiscardUnknown()
	featureValues.GetValues()
	bytes, err = featureValues.Marshal()
	require.NoError(t, err)
	err = featureValues.Unmarshal(bytes)
	require.NoError(t, err)
	featureValues.Reset()

	containerResponseEntries := &ContainerResponseEntries{
		ContainerInferenceResults: map[string]*InferenceResults{
			"container1": &InferenceResults{
				InferenceResults: []*InferenceResult{
					{
						IsDefault:     false,
						InferenceType: InferenceType_ClassificationOverload,
						Output:        55,
						Percentile:    60,
					},
					{
						IsDefault:     false,
						InferenceType: InferenceType_LatencyRegression,
						Output:        55,
						Percentile:    60,
					},
				},
			},
		},
	}
	_ = containerResponseEntries.String()
	containerResponseEntries.ProtoMessage()
	containerResponseEntries.XXX_Size()
	containerResponseEntries.XXX_Merge(containerResponseEntries)
	containerResponseEntries.XXX_Unmarshal(nil)
	containerResponseEntries.XXX_DiscardUnknown()
	containerResponseEntries.GetContainerInferenceResults()
	bytes, err = containerResponseEntries.Marshal()
	require.NoError(t, err)
	err = containerResponseEntries.Unmarshal(bytes)
	require.NoError(t, err)
	containerResponseEntries.Reset()

	inferenceResultClassification := &InferenceResult{
		IsDefault:     false,
		InferenceType: InferenceType_ClassificationOverload,
		Output:        55,
		Percentile:    60,
	}
	_ = inferenceResultClassification.String()
	inferenceResultClassification.ProtoMessage()
	inferenceResultClassification.XXX_Size()
	inferenceResultClassification.XXX_Merge(inferenceResultClassification)
	inferenceResultClassification.XXX_Unmarshal(nil)
	inferenceResultClassification.XXX_DiscardUnknown()
	inferenceResultClassification.GetIsDefault()
	inferenceResultClassification.GetInferenceType()
	inferenceResultClassification.GetOutput()
	inferenceResultClassification.GetPercentile()
	bytes, err = inferenceResultClassification.Marshal()
	require.NoError(t, err)
	err = inferenceResultClassification.Unmarshal(bytes)
	require.NoError(t, err)
	inferenceResultClassification.Reset()
}

func TestInference(t *testing.T) {
	t.Parallel()

	testServer := &UnimplementedInferenceServiceServer{}
	testServer.Inference(context.Background(), &InferenceRequest{})
}
