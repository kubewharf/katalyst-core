//go:build !amd64 || !linux
// +build !amd64 !linux

package dynamicpolicy

import (
	"context"
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func MigratePagesForContainer(ctx context.Context, podUID, containerId string,
	numasCount int, sourceNUMAs, destNUMAs machine.CPUSet) error {
	return fmt.Errorf("unsupported MigratePagesForContainer")
}
