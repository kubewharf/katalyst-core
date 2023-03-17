package region

import (
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

type QoSRegionEmpty struct {
	*QoSRegionBase
}

// NewQoSRegionEmpty returns a empty qos region instance, which containers no guaranteed containers
func NewQoSRegionEmpty(name string, numaLimit int,
	metaCache *metacache.MetaCache, emitter metrics.MetricEmitter) QoSRegion {

	numaIDs := sets.NewInt()
	for numaID := 0; numaID < numaLimit; numaID++ {
		numaIDs.Insert(numaID)
	}

	r := &QoSRegionEmpty{
		QoSRegionBase: NewQoSRegionBase(name, "", types.QoSRegionTypeEmpty, types.QosRegionPriorityDedicated, "", nil, numaIDs, metaCache, emitter),
	}
	return r
}

func (r *QoSRegionEmpty) TryUpdateControlKnob() error {
	return nil
}

func (r *QoSRegionEmpty) GetControlKnobUpdated() (types.ControlKnob, error) {
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	return types.ControlKnob{
		types.ControlKnobReclaimedCPUSetSize: types.ControlKnobValue{
			Value:  float64(r.cpuLimit - r.reservePoolSize),
			Action: types.ControlKnobActionNone,
		},
	}, nil
}

func (r *QoSRegionEmpty) GetHeadroom() (resource.Quantity, error) {
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	return *resource.NewQuantity(int64(r.cpuLimit-r.reservePoolSize), resource.DecimalSI), nil
}

func (r *QoSRegionEmpty) TryUpdateHeadroom() error {
	return nil
}

func (r *QoSRegionEmpty) IsEmpty() bool {
	return false
}
