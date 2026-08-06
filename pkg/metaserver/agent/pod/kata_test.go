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

package pod

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

var (
	testKataJsonInfo    = `{"sandboxID": "12345678", "pid": 1234, "runtimeType": "io.containerd.kata.v2"}`
	testInvalidJsonInfo = `{"pid: 2345"}` // no sandbox id field
	testNonKataJsonInfo = `{"sandboxID": "234567890", "pid": "2345", "runtimeType": "docker"}`
)

func TestKataContainerFetcher_getKataCgroupPathSuffix(t *testing.T) {
	t.Parallel()

	type fields struct {
		podUid            string
		containerId       string
		containerIdToInfo map[string]map[string]string
	}

	tests := []struct {
		name                 string
		fields               fields
		wantCgroupPathSuffix string
		wantSkip             bool
		wantErr              bool
	}{
		{
			name: "Cannot find container info",
			fields: fields{
				podUid:      "12345678",
				containerId: "invalidContainerId",
				containerIdToInfo: map[string]map[string]string{
					"container1234": {
						"info": testKataJsonInfo,
					},
				},
			},
			wantCgroupPathSuffix: "",
			wantSkip:             false,
			wantErr:              true,
		},
		{
			name: "Can find container info but cannot unmarshal json",
			fields: fields{
				podUid:      "12345678",
				containerId: "container1234",
				containerIdToInfo: map[string]map[string]string{
					"container1234": {
						"invalidField": testKataJsonInfo,
					},
				},
			},
			wantCgroupPathSuffix: "",
			wantSkip:             false,
			wantErr:              true,
		},
		{
			name: "Empty sandbox id",
			fields: fields{
				podUid:      "12345678",
				containerId: "container1234",
				containerIdToInfo: map[string]map[string]string{
					"container1234": {
						"info": testInvalidJsonInfo,
					},
				},
			},
			wantCgroupPathSuffix: "",
			wantSkip:             false,
			wantErr:              true,
		},
		{
			name: "Not kata container",
			fields: fields{
				podUid:      "12345678",
				containerId: "container1234",
				containerIdToInfo: map[string]map[string]string{
					"container1234": {
						"info": testNonKataJsonInfo,
					},
				},
			},
			wantCgroupPathSuffix: "",
			wantSkip:             true,
			wantErr:              false,
		},
		{
			name: "Can get the kata cgroup path suffix",
			fields: fields{
				podUid:      "12345678",
				containerId: "container1234",
				containerIdToInfo: map[string]map[string]string{
					"container1234": {
						"info": testKataJsonInfo,
					},
				},
			},
			wantCgroupPathSuffix: "pod12345678/kata_12345678",
			wantSkip:             false,
			wantErr:              false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			kataContainerFetcher := &KataContainerFetcher{
				runtimePodFetcher: &runtimePodFetcherStub{
					containerIdToInfo: tt.fields.containerIdToInfo,
				},
			}
			pathSuffix, skip, err := kataContainerFetcher.getKataCgroupPathSuffix(tt.fields.podUid, tt.fields.containerId)
			if (err != nil) != tt.wantErr {
				t.Errorf("getKataCgroupPathSuffix() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if skip != tt.wantSkip {
				t.Errorf("getKataCgroupPathSuffix() skip = %v, want %v", skip, tt.wantSkip)
			}
			if pathSuffix != tt.wantCgroupPathSuffix {
				t.Errorf("getKataCgroupPathSuffix() pathSuffix = %v, want %v", pathSuffix, tt.wantCgroupPathSuffix)
			}
		})
	}
}

func TestKataContainerFetcher_getKataContainerCgroupPathSkipsNonKataRuntime(t *testing.T) {
	t.Parallel()

	kataContainerFetcher := &KataContainerFetcher{
		runtimePodFetcher: &runtimePodFetcherStub{
			containerIdToInfo: map[string]map[string]string{
				"container1234": {
					"info": testNonKataJsonInfo,
				},
			},
		},
	}

	absPath, skip, err := kataContainerFetcher.getKataContainerAbsoluteCgroupPath("cpu", "12345", "container1234")
	assert.Empty(t, absPath)
	assert.True(t, skip)
	assert.NoError(t, err)

	relPath, skip, err := kataContainerFetcher.getKataContainerRelativeCgroupPath("12345", "container1234")
	assert.Empty(t, relPath)
	assert.True(t, skip)
	assert.NoError(t, err)
}

func TestKataContainerFetcher_getKataContainerAbsoluteCgroupPath(t *testing.T) {
	t.Parallel()

	containerIdToInfo := map[string]map[string]string{
		"container1234": {
			"info": testInvalidJsonInfo,
		},
	}

	kataContainerFetcher := &KataContainerFetcher{
		runtimePodFetcher: &runtimePodFetcherStub{
			containerIdToInfo: containerIdToInfo,
		},
	}

	absPath, skip, err := kataContainerFetcher.getKataContainerAbsoluteCgroupPath("cpu", "12345", "123456")
	assert.Equal(t, absPath, "")
	assert.False(t, skip)
	assert.NotNil(t, err)
}

func TestKataContainerFetcher_getKataContainerRelativeCgroupPath(t *testing.T) {
	t.Parallel()

	containerIdToInfo := map[string]map[string]string{
		"container1234": {
			"info": testInvalidJsonInfo,
		},
	}

	kataContainerFetcher := &KataContainerFetcher{
		runtimePodFetcher: &runtimePodFetcherStub{
			containerIdToInfo: containerIdToInfo,
		},
	}

	absPath, skip, err := kataContainerFetcher.getKataContainerRelativeCgroupPath("12345", "123456")
	assert.Equal(t, absPath, "")
	assert.False(t, skip)
	assert.NotNil(t, err)
}
