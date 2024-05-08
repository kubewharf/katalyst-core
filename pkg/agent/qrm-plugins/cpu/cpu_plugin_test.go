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

package cpu

import (
	"testing"
	"time"

	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/qrm"
	qrmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
)

func TestCPUOptions_ApplyTo(t *testing.T) {
	t.Parallel()

	type fields struct {
		PolicyName              string
		ReservedCPUCores        int
		SkipCPUStateCorruption  bool
		CPUDynamicPolicyOptions qrm.CPUDynamicPolicyOptions
		CPUNativePolicyOptions  qrm.CPUNativePolicyOptions
	}
	type args struct {
		conf *qrmconfig.CPUQRMPluginConfig
	}
	type mbmOptions struct {
		EnableMBM              bool
		MBMThresholdPercentage int
		MBMScanInterval        time.Duration
	}
	tests := []struct {
		name           string
		fields         fields
		args           args
		wantMBMOptions mbmOptions
		wantErr        bool
	}{
		{
			name: "happy path of mbm options",
			fields: fields{
				PolicyName: "dummy-policy",
				CPUDynamicPolicyOptions: qrm.CPUDynamicPolicyOptions{
					EnableMBM:              true,
					MBMThresholdPercentage: 88,
					MBMScanInterval:        time.Second * 2,
				},
				CPUNativePolicyOptions: qrm.CPUNativePolicyOptions{},
			},
			args: args{
				conf: &qrmconfig.CPUQRMPluginConfig{},
			},
			wantMBMOptions: mbmOptions{
				EnableMBM:              true,
				MBMThresholdPercentage: 88,
				MBMScanInterval:        time.Second * 2,
			},
			wantErr: false,
		},
		{
			name: "invalid options is corrected",
			fields: fields{
				PolicyName: "dummy",
				CPUDynamicPolicyOptions: qrm.CPUDynamicPolicyOptions{
					EnableMBM:              true,
					MBMThresholdPercentage: -1,
					MBMScanInterval:        time.Nanosecond * 1,
				},
				CPUNativePolicyOptions: qrm.CPUNativePolicyOptions{},
			},
			args: args{
				conf: &qrmconfig.CPUQRMPluginConfig{},
			},
			wantMBMOptions: mbmOptions{
				EnableMBM:              true,
				MBMThresholdPercentage: 50,
				MBMScanInterval:        time.Millisecond * 50,
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := &qrm.CPUOptions{
				PolicyName:              tt.fields.PolicyName,
				ReservedCPUCores:        tt.fields.ReservedCPUCores,
				SkipCPUStateCorruption:  tt.fields.SkipCPUStateCorruption,
				CPUDynamicPolicyOptions: tt.fields.CPUDynamicPolicyOptions,
				CPUNativePolicyOptions:  tt.fields.CPUNativePolicyOptions,
			}
			err := o.ApplyTo(tt.args.conf)
			if (err != nil) != tt.wantErr {
				t.Errorf("ApplyTo() error = %v, wantErr %v", err, tt.wantErr)
			}

			if err == nil {
				if tt.args.conf.EnableMBM != tt.wantMBMOptions.EnableMBM {
					t.Errorf("apply EnableMBM failed: expected %v, got %v", tt.args.conf.EnableMBM, tt.args.conf.EnableMBM)
				}
				if tt.args.conf.MBMThresholdPercentage != tt.wantMBMOptions.MBMThresholdPercentage {
					t.Errorf("apply MBMThresholdPercentage failed: expected %v, got %v",
						tt.wantMBMOptions.MBMThresholdPercentage, tt.args.conf.MBMThresholdPercentage)
				}
				if tt.args.conf.MBMScanInterval != tt.wantMBMOptions.MBMScanInterval {
					t.Errorf("apply MBMScanInterval failed: expected %v, got %v",
						tt.wantMBMOptions.MBMScanInterval, tt.args.conf.MBMScanInterval)
				}
			}
		})
	}
}

func TestCPUOptions_AddFlags(t *testing.T) {
	t.Parallel()

	o := qrm.NewCPUOptions()
	fss := &cliflag.NamedFlagSets{}
	o.AddFlags(fss)

	fs := fss.FlagSet("cpu_resource_plugin")
	if err := fs.Parse([]string{
		"--enable-mbm",
		"--mbm-scan-interval=5s",
	}); err != nil {
		t.Errorf("addflags failed, error: %#v", err)
		return
	}

	if !o.EnableMBM {
		t.Errorf("mbm enabling: expected true, got %v", o.EnableMBM)
	}

	if o.MBMScanInterval != time.Second*5 {
		t.Errorf("mbm scan interval: expected %v, got %v", time.Second*5, o.MBMScanInterval)
	}
}
