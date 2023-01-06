//go:build linux && arm64
// +build linux,arm64

package nvidia

import (
	"github.com/pkg/errors"
)

// getGPUMemory get gpu memory by loading stats from nvml.
func GetGPUMemory() (string, error) {
	return "", errors.New("arm64 not supported")
}
