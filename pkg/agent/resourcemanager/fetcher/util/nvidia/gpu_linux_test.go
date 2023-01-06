//go:build linux && !arm64
// +build linux,!arm64

package nvidia

import (
	"testing"

	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/nvml"
)

func makeGPUDevice(mem uint64) *nvml.Device {
	return &nvml.Device{
		Memory: &mem,
	}
}

func TestParseGPUMemory(t *testing.T) {
	tests := []struct {
		name   string
		device *nvml.Device
		mem    string
		err    string
	}{
		{"card is too small", makeGPUDevice(1024 * 1), "", "get gpu memory error, too small"},
		{"16Gi", makeGPUDevice(1024 * 13), "16Gi", ""},
		{"24Gi", makeGPUDevice(1024 * 22), "24Gi", ""},
		{"32Gi", makeGPUDevice(1024 * 30), "32Gi", ""},
		{"40Gi", makeGPUDevice(1024 * 40), "64Gi", ""},
		{"64Gi", makeGPUDevice(1024 * 56), "64Gi", ""},
		{"80Gi", makeGPUDevice(1000 * 64), "80Gi", ""},
		{"80Gi", makeGPUDevice(1024 * 64), "80Gi", ""},
		{"80Gi", makeGPUDevice(1024 * 80), "80Gi", ""},
		{"card is too big", makeGPUDevice(1024 * 100), "", "get gpu memory error, too big"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mem, err := parseGPUMemory(tt.device)
			if err != nil {
				if err.Error() != tt.err {
					t.Errorf("parseGPUMemory() error = %v, wantErr %v", err, tt.err)
				}
				return
			}
			if mem != tt.mem {
				t.Errorf("parseGPUMemory() = %v, want %v", mem, tt.mem)
			}
		})
	}
}
