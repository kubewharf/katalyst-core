package test

import (
	"reflect"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/mbw/utils/pci"
)

func TestPCIDev_GetDevInfo(t *testing.T) {
	t.Parallel()
	var devTest = &pci.PCIDev{}
	got := devTest.GetDevInfo()
	if 0 != got.DeviceID {
		t.Errorf("expected dev id 11, got %d", got.DeviceID)
	}
}

func TestPCIDev_BDFString(t *testing.T) {
	t.Parallel()
	var devTest = &pci.PCIDev{}

	got := devTest.BDFString()
	want := "0000:00:00.0"
	if want != got {
		t.Errorf("expected %s, got %s", want, got)
	}
}

func TestPCIDev_GetDevNumaNode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		dev  *pci.PCIDev
		want int
	}{
		{
			name: "negative path returns -1",
			dev:  &pci.PCIDev{},
			want: -1,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.dev.GetDevNumaNode(); got != tt.want {
				t.Errorf("GetDevNumaNode() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPCIDev_Init_Cleanup(t *testing.T) {
	t.Parallel()

	// this test does not verify specific attributes,
	// but the general init/cleanup behavior able to run
	pci.PCIDevInit()
	pci.PCIDevCleanup()
}

func TestGetFirstIOHC(t *testing.T) {
	t.Parallel()

	testNode := 1
	testDevs := []*pci.PCIDev{
		&pci.PCIDev{},
	}

	var want *pci.PCIDev = nil
	if got := pci.GetFirstIOHC(testNode, testDevs); !reflect.DeepEqual(got, want) {
		t.Errorf("GetFirstIOHC() = %v, want %v", got, want)
	}
}
