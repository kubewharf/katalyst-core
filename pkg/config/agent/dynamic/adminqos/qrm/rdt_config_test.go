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

package qrm

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/intstr"

	configv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

func TestQRMPluginConfigurationApplyRDTConfiguration(t *testing.T) {
	t.Parallel()
	disableRDT := true
	enableCPUList := true
	enableCAT := true
	defaultCATWays := intstr.FromString("MaxCATWays")
	exclusiveClosIDs := []string{"clos-a", "peer-b"}
	defaultAllowedBitUsages := []configv1alpha1.CATBitUsage{configv1alpha1.CATBitUsageAll}

	config := NewQRMPluginConfiguration()
	config.ApplyConfiguration(&crd.DynamicConfigCRD{
		AdminQoSConfiguration: &configv1alpha1.AdminQoSConfiguration{
			Spec: configv1alpha1.AdminQoSConfigurationSpec{
				Config: configv1alpha1.AdminQoSConfig{
					QRMPluginConfig: &configv1alpha1.QRMPluginConfig{
						RDTConfig: &configv1alpha1.RDTConfig{
							DisableRDT: &disableRDT,
						},
						CPUPluginConfig: &configv1alpha1.CPUPluginConfig{
							BulkheadConfig: &configv1alpha1.BulkheadConfig{
								BulkheadRDTConfig: &configv1alpha1.BulkheadRDTConfig{
									EnableCPUList:  &enableCPUList,
									EnableCAT:      &enableCAT,
									DefaultCATWays: &defaultCATWays,
									ClosCATWays: map[string]intstr.IntOrString{
										"share-00": intstr.FromString("MaxCATWays-MinCATWays"),
										"share-01": intstr.FromInt(2),
									},
									CATPolicy: &configv1alpha1.CATPolicy{
										DefaultPlacement: &configv1alpha1.CATPlacementPolicy{
											AllowedBitUsages: defaultAllowedBitUsages,
										},
										ExclusiveClosIDs: &exclusiveClosIDs,
									},
								},
							},
						},
					},
				},
			},
		},
	})

	if !config.RDTConfig.DisableRDT {
		t.Fatal("DisableRDT = false, want true")
	}
	bulkheadRDT := config.CPUPluginConfiguration.BulkheadConfig.BulkheadRDTConfig
	if !bulkheadRDT.EnableCPUList || !bulkheadRDT.EnableCAT {
		t.Fatalf("bulkhead RDT enables = %#v, want CPUList and CAT enabled", bulkheadRDT)
	}
	if got := bulkheadRDT.DefaultCATWays.String(); got != "MaxCATWays" {
		t.Fatalf("DefaultCATWays = %s, want MaxCATWays", got)
	}
	if got := bulkheadRDT.ClosCATWays["share-00"].String(); got != "MaxCATWays-MinCATWays" {
		t.Fatalf("ClosCATWays[share-00] = %s, want MaxCATWays-MinCATWays", got)
	}
	if got := bulkheadRDT.ClosCATWays["share-01"].String(); got != "2" {
		t.Fatalf("ClosCATWays[share-01] = %s, want 2", got)
	}
	if got := bulkheadRDT.CATPolicy.ExclusiveClosIDs; len(got) != 2 || got[0] != "clos-a" || got[1] != "peer-b" {
		t.Fatalf("ExclusiveClosIDs = %#v, want clos-a and peer-b", got)
	}
	if got := bulkheadRDT.CATPolicy.DefaultPlacement.AllowedBitUsages; len(got) != 1 || got[0] != CATBitUsageAll {
		t.Fatalf("DefaultPlacement.AllowedBitUsages = %#v, want *", got)
	}
}

func TestQRMPluginConfigurationMergesCATPolicy(t *testing.T) {
	t.Parallel()
	exclusiveClosIDs := []string{"clos-a"}
	config := NewQRMPluginConfiguration()
	config.CPUPluginConfiguration.BulkheadConfig.BulkheadRDTConfig.CATPolicy = CATPolicy{
		DefaultPlacement: &CATPlacementPolicy{
			AllowedBitUsages: []CATBitUsage{CATBitUsageSoftware},
			Direction:        CATAllocationDirectionHigh,
		},
	}

	config.ApplyConfiguration(&crd.DynamicConfigCRD{
		AdminQoSConfiguration: &configv1alpha1.AdminQoSConfiguration{
			Spec: configv1alpha1.AdminQoSConfigurationSpec{
				Config: configv1alpha1.AdminQoSConfig{
					QRMPluginConfig: &configv1alpha1.QRMPluginConfig{
						CPUPluginConfig: &configv1alpha1.CPUPluginConfig{
							BulkheadConfig: &configv1alpha1.BulkheadConfig{
								BulkheadRDTConfig: &configv1alpha1.BulkheadRDTConfig{
									CATPolicy: &configv1alpha1.CATPolicy{
										ExclusiveClosIDs: &exclusiveClosIDs,
									},
								},
							},
						},
					},
				},
			},
		},
	})

	bulkheadRDT := config.CPUPluginConfiguration.BulkheadConfig.BulkheadRDTConfig
	if got := bulkheadRDT.CATPolicy.ExclusiveClosIDs; len(got) != 1 || got[0] != "clos-a" {
		t.Fatalf("ExclusiveClosIDs = %#v, want clos-a", got)
	}
	placement := bulkheadRDT.CATPolicy.DefaultPlacement
	if placement == nil {
		t.Fatal("DefaultPlacement is nil, want inherited startup placement")
	}
	if got := placement.AllowedBitUsages; len(got) != 1 || got[0] != CATBitUsageSoftware {
		t.Fatalf("DefaultPlacement.AllowedBitUsages = %#v, want inherited S", got)
	}
	if placement.Direction != CATAllocationDirectionHigh {
		t.Fatalf("DefaultPlacement.Direction = %q, want inherited high", placement.Direction)
	}
}

func TestQRMPluginConfigurationRecordsInvalidCATExpression(t *testing.T) {
	t.Parallel()
	enableCAT := true
	defaultCATWays := intstr.FromString("MaxCATWays")

	config := NewQRMPluginConfiguration()
	config.ApplyConfiguration(&crd.DynamicConfigCRD{
		AdminQoSConfiguration: &configv1alpha1.AdminQoSConfiguration{
			Spec: configv1alpha1.AdminQoSConfigurationSpec{
				Config: configv1alpha1.AdminQoSConfig{
					QRMPluginConfig: &configv1alpha1.QRMPluginConfig{
						CPUPluginConfig: &configv1alpha1.CPUPluginConfig{
							BulkheadConfig: &configv1alpha1.BulkheadConfig{
								BulkheadRDTConfig: &configv1alpha1.BulkheadRDTConfig{
									EnableCAT:      &enableCAT,
									DefaultCATWays: &defaultCATWays,
									ClosCATWays: map[string]intstr.IntOrString{
										"share-00": intstr.FromString("invalid"),
									},
								},
							},
						},
					},
				},
			},
		},
	})

	bulkheadRDT := config.CPUPluginConfiguration.BulkheadConfig.BulkheadRDTConfig
	if bulkheadRDT.CATConfigError == "" {
		t.Fatal("CATConfigError is empty, want invalid expression error")
	}
	if bulkheadRDT.CATConfigError != strings.ToLower(bulkheadRDT.CATConfigError) {
		t.Fatalf("CATConfigError = %q, want lower-case error", bulkheadRDT.CATConfigError)
	}
	if bulkheadRDT.ClosCATWays != nil {
		t.Fatalf("ClosCATWays = %#v, want nil after invalid map conversion", bulkheadRDT.ClosCATWays)
	}
}

func TestQRMPluginConfigurationRDTDisableOverrideLifecycle(t *testing.T) {
	t.Parallel()

	// Startup seed: dynamic RDT config starts with DisableRDT=true.
	config := NewQRMPluginConfiguration()
	config.RDTConfig.DisableRDT = true

	// Apply AQC with nil RDTConfig → startup seed must be preserved.
	config.ApplyConfiguration(&crd.DynamicConfigCRD{
		AdminQoSConfiguration: &configv1alpha1.AdminQoSConfiguration{
			Spec: configv1alpha1.AdminQoSConfigurationSpec{
				Config: configv1alpha1.AdminQoSConfig{
					QRMPluginConfig: &configv1alpha1.QRMPluginConfig{
						RDTConfig: nil,
					},
				},
			},
		},
	})
	if !config.RDTConfig.DisableRDT {
		t.Fatal("after nil RDTConfig apply, DisableRDT = false, want preserved true")
	}

	// Apply AQC with RDTConfig but omitted DisableRDT pointer → seed must stay true.
	config.ApplyConfiguration(&crd.DynamicConfigCRD{
		AdminQoSConfiguration: &configv1alpha1.AdminQoSConfiguration{
			Spec: configv1alpha1.AdminQoSConfigurationSpec{
				Config: configv1alpha1.AdminQoSConfig{
					QRMPluginConfig: &configv1alpha1.QRMPluginConfig{
						RDTConfig: &configv1alpha1.RDTConfig{
							DisableRDT: nil,
						},
					},
				},
			},
		},
	})
	if !config.RDTConfig.DisableRDT {
		t.Fatal("after omitted DisableRDT apply, DisableRDT = false, want preserved true")
	}

	// Explicit pointer false overrides seed to false.
	disableRDTFalse := false
	config.ApplyConfiguration(&crd.DynamicConfigCRD{
		AdminQoSConfiguration: &configv1alpha1.AdminQoSConfiguration{
			Spec: configv1alpha1.AdminQoSConfigurationSpec{
				Config: configv1alpha1.AdminQoSConfig{
					QRMPluginConfig: &configv1alpha1.QRMPluginConfig{
						RDTConfig: &configv1alpha1.RDTConfig{
							DisableRDT: &disableRDTFalse,
						},
					},
				},
			},
		},
	})
	if config.RDTConfig.DisableRDT {
		t.Fatal("after explicit false apply, DisableRDT = true, want false")
	}

	// Explicit pointer true sets back to true.
	disableRDTTrue := true
	config.ApplyConfiguration(&crd.DynamicConfigCRD{
		AdminQoSConfiguration: &configv1alpha1.AdminQoSConfiguration{
			Spec: configv1alpha1.AdminQoSConfigurationSpec{
				Config: configv1alpha1.AdminQoSConfig{
					QRMPluginConfig: &configv1alpha1.QRMPluginConfig{
						RDTConfig: &configv1alpha1.RDTConfig{
							DisableRDT: &disableRDTTrue,
						},
					},
				},
			},
		},
	})
	if !config.RDTConfig.DisableRDT {
		t.Fatal("after explicit true apply, DisableRDT = false, want true")
	}
}
