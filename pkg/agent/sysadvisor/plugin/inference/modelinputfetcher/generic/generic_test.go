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

package generic

import (
	"io/ioutil"
	"os"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
)

func TestSetFeatureValueFuncs(t *testing.T) {
	t.Parallel()

	// backup original functions
	originalGetNodeFeatureValue := getNodeFeatureValue
	originalGetNumaFeatureValue := getNumaFeatureValue
	originalGetContainerFeatureValue := getContainerFeatureValue

	// restore original functions after test
	defer func() {
		SetGetNodeFeatureValueFunc(originalGetNodeFeatureValue)
		SetGetNumaFeatureValueFunc(originalGetNumaFeatureValue)
		SetGetContainerFeatureValueFunc(originalGetContainerFeatureValue)
	}()

	nodeCalled := false
	SetGetNodeFeatureValueFunc(func(timestamp int64, metaServer *metaserver.MetaServer, metaReader metacache.MetaReader, conf *config.Configuration, extraConf interface{}) (map[string]interface{}, error) {
		nodeCalled = true
		return nil, nil
	})

	numaCalled := false
	SetGetNumaFeatureValueFunc(func(timestamp int64, numaID int, metaServer *metaserver.MetaServer, metaReader metacache.MetaReader, conf *config.Configuration, extraConf interface{}) (map[string]interface{}, error) {
		numaCalled = true
		return nil, nil
	})

	containerCalled := false
	SetGetContainerFeatureValueFunc(func(timestamp int64, podUID string, containerName string, metaServer *metaserver.MetaServer, metaReader metacache.MetaReader, conf *config.Configuration, extraConf interface{}) (map[string]interface{}, error) {
		containerCalled = true
		return nil, nil
	})

	_, _ = getNodeFeatureValue(0, nil, nil, nil, nil)
	if !nodeCalled {
		t.Errorf("getNodeFeatureValue was not called")
	}

	_, _ = getNumaFeatureValue(0, 0, nil, nil, nil, nil)
	if !numaCalled {
		t.Errorf("getNumaFeatureValue was not called")
	}

	_, _ = getContainerFeatureValue(0, "", "", nil, nil, nil, nil)
	if !containerCalled {
		t.Errorf("getContainerFeatureValue was not called")
	}
}

func TestNewGenericModelInputFetcher(t *testing.T) {
	t.Parallel()

	newConf := func() *config.Configuration {
		conf := config.NewConfiguration()
		return conf
	}

	type args struct {
		fetcherName string
		conf        *config.Configuration
		extraConf   interface{}
		emitterPool metricspool.MetricsEmitterPool
		metaServer  *metaserver.MetaServer
		metaCache   metacache.MetaCache
	}
	tests := []struct {
		name    string
		args    args
		wantNil bool
		wantErr bool
	}{
		{
			name: "normal case",
			args: args{
				fetcherName: "test",
				conf:        newConf(),
				emitterPool: metricspool.DummyMetricsEmitterPool{},
				metaServer:  &metaserver.MetaServer{},
			},
			wantErr: false,
		},
		{
			name: "nil conf",
			args: args{
				conf: nil,
			},
			wantErr: true,
		},
		{
			name: "nil metaserver",
			args: args{
				conf:       newConf(),
				metaServer: nil,
			},
			wantErr: true,
		},
		{
			name: "nil metacache",
			args: args{
				conf:       newConf(),
				metaServer: &metaserver.MetaServer{},
				metaCache:  nil,
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if tt.args.conf != nil {
				stateFileDir, err := ioutil.TempDir("", "metacache-TestNewGenericModelInputFetcher")
				if err != nil {
					t.Fatalf("failed to create temp dir: %v", err)
				}
				defer os.RemoveAll(stateFileDir)
				tt.args.conf.GenericSysAdvisorConfiguration.StateFileDirectory = stateFileDir
			}

			if tt.args.metaCache == nil && tt.name != "nil metacache" && tt.args.conf != nil {
				mc, err := metacache.NewMetaCacheImp(tt.args.conf, metricspool.DummyMetricsEmitterPool{}, nil)
				if err != nil {
					t.Fatalf("failed to create metacache: %v", err)
				}
				tt.args.metaCache = mc
			}
			if tt.args.emitterPool == nil {
				tt.args.emitterPool = metricspool.DummyMetricsEmitterPool{}
			}

			got, err := NewGenericModelInputFetcher(tt.args.fetcherName, tt.args.conf, tt.args.extraConf, tt.args.emitterPool, tt.args.metaServer, tt.args.metaCache)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewGenericModelInputFetcher() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantNil {
				if got != nil {
					t.Errorf("NewGenericModelInputFetcher() got = %v, want nil", got)
				}
			} else if !tt.wantErr {
				if got == nil {
					t.Errorf("NewGenericModelInputFetcher() got nil, want non-nil")
				}
			}
		})
	}
}
