package resourcepackage

import (
	"context"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/npd"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

type ResourcePackageManager interface {
	// GetResourcePackages returns the resource packages for the CustomNodeResource
	GetResourcePackages(cnr *nodev1alpha1.CustomNodeResource) map[int][]nodev1alpha1.ResourcePackage
}

type DummyResourcePackageManager struct{}

type resourcePackageManager struct {
	fetcher npd.NPDFetcher
}

func NewResourcePackageManager(fetcher npd.NPDFetcher) ResourcePackageManager {
	return &resourcePackageManager{
		fetcher: fetcher,
	}
}

func (r *resourcePackageManager) GetResourcePackages(cnr *nodev1alpha1.CustomNodeResource) map[int][]nodev1alpha1.ResourcePackage {
	return map[int][]nodev1alpha1.ResourcePackage{
		0: {
			{
				PackageName: "x2",
				Allocatable: &v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("32"),
					v1.ResourceMemory: resource.MustParse("64Gi"),
				},
			},
			{
				PackageName: "x8",
				Allocatable: &v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("16"),
					v1.ResourceMemory: resource.MustParse("128Gi"),
				},
			},
		},
		1: {
			{
				PackageName: "x2",
				Allocatable: &v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("32"),
					v1.ResourceMemory: resource.MustParse("64Gi"),
				},
			},
			{
				PackageName: "x8",
				Allocatable: &v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("16"),
					v1.ResourceMemory: resource.MustParse("128Gi"),
				},
			},
		},
	}
}

func NewDummyResourcePackageManager() *DummyResourcePackageManager {
	return &DummyResourcePackageManager{}
}

func (d *DummyResourcePackageManager) GetResourcePackages(cnr *nodev1alpha1.CustomNodeResource) map[int][]nodev1alpha1.ResourcePackage {
	return map[int][]nodev1alpha1.ResourcePackage{
		0: {
			{
				PackageName: "x2",
				Allocatable: &v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("32"),
					v1.ResourceMemory: resource.MustParse("64Gi"),
				},
			},
			{
				PackageName: "x8",
				Allocatable: &v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("16"),
					v1.ResourceMemory: resource.MustParse("128Gi"),
				},
			},
		},
		1: {
			{
				PackageName: "x2",
				Allocatable: &v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("32"),
					v1.ResourceMemory: resource.MustParse("64Gi"),
				},
			},
			{
				PackageName: "x8",
				Allocatable: &v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("16"),
					v1.ResourceMemory: resource.MustParse("128Gi"),
				},
			},
		},
	}
}

func (d *DummyResourcePackageManager) Run(_ context.Context) {}
