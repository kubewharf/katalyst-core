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

package resourcepackage

import (
	"reflect"
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
)

func TestGetResourcePackageName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		annotations map[string]string
		want        string
	}{
		{
			name: "normal case with resource package annotation",
			annotations: map[string]string{
				consts.PodAnnotationResourcePackageKey: "test-resource-package",
			},
			want: "test-resource-package",
		},
		{
			name:        "empty annotations map",
			annotations: map[string]string{},
			want:        "",
		},
		{
			name:        "nil annotations map",
			annotations: nil,
			want:        "",
		},
		{
			name: "annotations map without resource package key",
			annotations: map[string]string{
				"other-key": "other-value",
			},
			want: "",
		},
		{
			name: "resource package key with empty value",
			annotations: map[string]string{
				consts.PodAnnotationResourcePackageKey: "",
			},
			want: "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := GetResourcePackageName(tt.annotations); got != tt.want {
				t.Errorf("GetResourcePackageName() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWrapOwnerPoolName(t *testing.T) {
	t.Parallel()
	type args struct {
		ownerPoolName string
		pkgName       string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "empty package name",
			args: args{
				ownerPoolName: "pool1",
				pkgName:       "",
			},
			want: "pool1",
		},
		{
			name: "normal package name",
			args: args{
				ownerPoolName: "pool1",
				pkgName:       "pkg1",
			},
			want: "pkg1/pool1",
		},
		{
			name: "empty owner pool name",
			args: args{
				ownerPoolName: "",
				pkgName:       "pkg1",
			},
			want: "pkg1/",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := WrapOwnerPoolName(tt.args.ownerPoolName, tt.args.pkgName); got != tt.want {
				t.Errorf("WrapOwnerPoolName() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUnwrapOwnerPoolName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		ownerPoolName     string
		wantPkgName       string
		wantOwnerPoolName string
	}{
		{
			name:              "normal wrapped name",
			ownerPoolName:     "pkg1/pool1",
			wantPkgName:       "pkg1",
			wantOwnerPoolName: "pool1",
		},
		{
			name:              "no separator",
			ownerPoolName:     "pool1",
			wantPkgName:       "",
			wantOwnerPoolName: "pool1",
		},
		{
			name:              "empty string",
			ownerPoolName:     "",
			wantPkgName:       "",
			wantOwnerPoolName: "",
		},
		{
			name:              "separator at the end",
			ownerPoolName:     "pkg1/",
			wantPkgName:       "pkg1",
			wantOwnerPoolName: "",
		},
		{
			name:              "separator at the beginning",
			ownerPoolName:     "/pool1",
			wantPkgName:       "",
			wantOwnerPoolName: "pool1",
		},
		{
			name:              "multiple separators",
			ownerPoolName:     "group/pkg1/pool1",
			wantPkgName:       "group/pkg1",
			wantOwnerPoolName: "pool1",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotOwner, gotPkg := UnwrapOwnerPoolName(tt.ownerPoolName)
			if gotPkg != tt.wantPkgName {
				t.Errorf("UnwrapOwnerPoolName() gotPkg = %v, want %v", gotPkg, tt.wantPkgName)
			}
			if gotOwner != tt.wantOwnerPoolName {
				t.Errorf("UnwrapOwnerPoolName() gotOwner = %v, want %v", gotOwner, tt.wantOwnerPoolName)
			}
		})
	}
}

func TestGetOwnerPoolName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		ownerPoolName string
		want          string
	}{
		{
			name:          "normal wrapped name",
			ownerPoolName: "pkg1/pool1",
			want:          "pool1",
		},
		{
			name:          "no separator",
			ownerPoolName: "pool1",
			want:          "pool1",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := GetOwnerPoolName(tt.ownerPoolName); got != tt.want {
				t.Errorf("GetOwnerPoolName() = %v, want %v", got, tt.want)
			}
		})
	}
}

// MockSuffixTranslator for testing ResourcePackageSuffixTranslatorWrapper
type mockSuffixTranslator struct{}

func (m *mockSuffixTranslator) Translate(s string) string {
	return "translated-" + s
}

func TestResourcePackageSuffixTranslatorWrapper(t *testing.T) {
	t.Parallel()
	mock := &mockSuffixTranslator{}
	wrapper := ResourcePackageSuffixTranslatorWrapper(mock)

	tests := []struct {
		name          string
		ownerPoolName string
		want          string
	}{
		{
			name:          "wrapped name",
			ownerPoolName: "pkg1/pool1",
			want:          "translated-pool1",
		},
		{
			name:          "unwrapped name",
			ownerPoolName: "pool1",
			want:          "translated-pool1",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := wrapper.Translate(tt.ownerPoolName); got != tt.want {
				t.Errorf("Translate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func boolPtr(b bool) *bool {
	return &b
}

func makeResourcePackageItem(pinned *bool, cpuValue int64) ResourcePackageItem {
	item := ResourcePackageItem{
		ResourcePackage: nodev1alpha1.ResourcePackage{
			Allocatable: &v1.ResourceList{
				v1.ResourceCPU: *resource.NewQuantity(cpuValue, resource.DecimalSI),
			},
		},
	}
	if pinned != nil {
		item.Config = &ResourcePackageConfig{
			PinnedCPUSet: pinned,
		}
	}
	return item
}

func TestNUMAResourcePackageItems_GetResourcePackageConfig(t *testing.T) {
	t.Parallel()

	items := NUMAResourcePackageItems{
		0: {
			"pkg1": makeResourcePackageItem(boolPtr(true), 10),
		},
	}

	type args struct {
		numaID  int
		pkgName string
	}
	tests := []struct {
		name    string
		r       NUMAResourcePackageItems
		args    args
		want    *ResourcePackageConfig
		wantErr bool
	}{
		{
			name:    "nil receiver",
			r:       nil,
			args:    args{numaID: 0, pkgName: "pkg1"},
			want:    nil,
			wantErr: true,
		},
		{
			name:    "numaID not found",
			r:       items,
			args:    args{numaID: 1, pkgName: "pkg1"},
			want:    nil,
			wantErr: true,
		},
		{
			name:    "pkgName not found",
			r:       items,
			args:    args{numaID: 0, pkgName: "pkg2"},
			want:    nil,
			wantErr: true,
		},
		{
			name:    "success",
			r:       items,
			args:    args{numaID: 0, pkgName: "pkg1"},
			want:    items[0]["pkg1"].Config,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := tt.r.GetResourcePackageConfig(tt.args.numaID, tt.args.pkgName)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetResourcePackageConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetResourcePackageConfig() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNUMAResourcePackageItems_GetPinnedCPUSetSize(t *testing.T) {
	t.Parallel()

	pinnedTrue := true
	pinnedFalse := false

	items := NUMAResourcePackageItems{
		0: {
			"pinned":   makeResourcePackageItem(&pinnedTrue, 10),
			"unpinned": makeResourcePackageItem(&pinnedFalse, 10),
			"noConfig": makeResourcePackageItem(nil, 10),
			"noAlloc": ResourcePackageItem{
				ResourcePackage: nodev1alpha1.ResourcePackage{Allocatable: nil},
				Config:          &ResourcePackageConfig{PinnedCPUSet: &pinnedTrue},
			},
		},
	}

	type args struct {
		numaID  int
		pkgName string
	}
	tests := []struct {
		name    string
		r       NUMAResourcePackageItems
		args    args
		want    *int
		wantErr bool
	}{
		{
			name:    "nil receiver",
			r:       nil,
			args:    args{numaID: 0, pkgName: "pinned"},
			want:    nil,
			wantErr: true,
		},
		{
			name:    "numaID not found",
			r:       items,
			args:    args{numaID: 1, pkgName: "pinned"},
			want:    nil,
			wantErr: true,
		},
		{
			name:    "pkgName not found",
			r:       items,
			args:    args{numaID: 0, pkgName: "missing"},
			want:    nil,
			wantErr: true,
		},
		{
			name:    "not pinned (false)",
			r:       items,
			args:    args{numaID: 0, pkgName: "unpinned"},
			want:    nil,
			wantErr: false,
		},
		{
			name:    "no config",
			r:       items,
			args:    args{numaID: 0, pkgName: "noConfig"},
			want:    nil,
			wantErr: false,
		},
		{
			name:    "allocatable nil",
			r:       items,
			args:    args{numaID: 0, pkgName: "noAlloc"},
			want:    nil,
			wantErr: true,
		},
		{
			name:    "success",
			r:       items,
			args:    args{numaID: 0, pkgName: "pinned"},
			want:    func() *int { i := 10; return &i }(),
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := tt.r.GetPinnedCPUSetSize(tt.args.numaID, tt.args.pkgName)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetPinnedCPUSetSize() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetPinnedCPUSetSize() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNUMAResourcePackageItems_ListAllPinnedCPUSetSize(t *testing.T) {
	t.Parallel()

	pinnedTrue := true
	pinnedFalse := false

	items := NUMAResourcePackageItems{
		0: {
			"pkg1": makeResourcePackageItem(&pinnedTrue, 10),
			"pkg2": makeResourcePackageItem(&pinnedFalse, 5),
			"pkg3": makeResourcePackageItem(nil, 5),
		},
		1: {
			"pkg4": makeResourcePackageItem(&pinnedTrue, 20),
			"pkg5": ResourcePackageItem{
				ResourcePackage: nodev1alpha1.ResourcePackage{Allocatable: nil},
				Config:          &ResourcePackageConfig{PinnedCPUSet: &pinnedTrue},
			},
		},
	}

	tests := []struct {
		name    string
		r       NUMAResourcePackageItems
		want    map[int]map[string]int
		wantErr bool
	}{
		{
			name:    "nil receiver",
			r:       nil,
			want:    nil,
			wantErr: true,
		},
		{
			name: "list all",
			r:    items,
			want: map[int]map[string]int{
				0: {
					"pkg1": 10,
				},
				1: {
					"pkg4": 20,
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := tt.r.ListAllPinnedCPUSetSize()
			if (err != nil) != tt.wantErr {
				t.Errorf("ListAllPinnedCPUSetSize() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ListAllPinnedCPUSetSize() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNUMAResourcePackageItems_GetAllPinnedCPUSetSizeSum(t *testing.T) {
	t.Parallel()

	pinnedTrue := true
	pinnedFalse := false

	items := NUMAResourcePackageItems{
		0: {
			"pkg1": makeResourcePackageItem(&pinnedTrue, 10),
			"pkg2": makeResourcePackageItem(&pinnedTrue, 5),
			"pkg3": makeResourcePackageItem(&pinnedFalse, 100),
		},
		1: {
			"pkg4": makeResourcePackageItem(&pinnedTrue, 20),
		},
	}

	tests := []struct {
		name    string
		r       NUMAResourcePackageItems
		want    map[int]int
		wantErr bool
	}{
		{
			name:    "nil receiver",
			r:       nil,
			want:    nil,
			wantErr: true,
		},
		{
			name: "sum all",
			r:    items,
			want: map[int]int{
				0: 15, // 10 + 5
				1: 20,
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := tt.r.GetAllPinnedCPUSetSizeSum()
			if (err != nil) != tt.wantErr {
				t.Errorf("GetAllPinnedCPUSetSizeSum() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetAllPinnedCPUSetSizeSum() got = %v, want %v", got, tt.want)
			}
		})
	}
}
