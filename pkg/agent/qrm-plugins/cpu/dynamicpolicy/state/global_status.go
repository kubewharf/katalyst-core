package state

import (
	"fmt"
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

var (
	allowSharedCoresOverlapReclaimedCores     *bool
	allowSharedCoresOverlapReclaimedCoresLock sync.RWMutex
)

func GetAllowSharedCoresOverlapReclaimedCores() (bool, error) {
	allowSharedCoresOverlapReclaimedCoresLock.RLock()
	defer allowSharedCoresOverlapReclaimedCoresLock.RUnlock()

	if allowSharedCoresOverlapReclaimedCores == nil {
		return false, fmt.Errorf("allowSharedCoresOverlapReclaimedCores isn't set")
	}
	return *allowSharedCoresOverlapReclaimedCores, nil
}

func SetAllowSharedCoresOverlapReclaimedCores(input bool) {
	general.Infof("set global allowSharedCoresOverlapReclaimedCores: %v", input)
	allowSharedCoresOverlapReclaimedCoresLock.Lock()
	defer allowSharedCoresOverlapReclaimedCoresLock.Unlock()

	copied := input
	allowSharedCoresOverlapReclaimedCores = &copied
}
