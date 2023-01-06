//go:build linux && !arm64
// +build linux,!arm64

package nvidia

import (
	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/nvml"
	"github.com/pkg/errors"
	"k8s.io/klog/v2"
)

// getGPUMemory get gpu memory by loading stats from nvml.
func GetGPUMemory() (string, error) {
	if err := nvml.Init(); err != nil {
		return "", errors.Wrap(err, "could not init nvml")
	}
	defer nvml.Shutdown()
	gpuCount, err := nvml.GetDeviceCount()
	if err != nil {
		return "", errors.Wrap(err, "could not get device count")
	}
	if gpuCount == 0 {
		return "", errors.New("no gpu device found")
	}
	device, err := nvml.NewDevice(0)
	if err != nil {
		return "", errors.Wrap(err, "could not get device 0")
	}
	return parseGPUMemory(device)
}

func parseGPUMemory(device *nvml.Device) (string, error) {
	mem := device.Memory
	if mem == nil {
		return "", errors.New("could not get memory info")
	}
	klog.Infof("gpu memory: %d", *mem)
	if *mem < 7000 {
		return "", errors.New("get gpu memory error, too small")
	} else if *mem < 17000 {
		return "16Gi", nil
	} else if *mem < 25000 {
		return "24Gi", nil
	} else if *mem < 32800 {
		return "32Gi", nil
	} else if *mem < 40960 {
		return "40Gi", nil
	} else if *mem < 64000 {
		return "64Gi", nil
	} else if *mem < 82000 {
		return "80Gi", nil
	} else {
		return "", errors.New("get gpu memory error, too big")
	}
}
