package podadmit

import (
	"encoding/json"
	"fmt"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
	qosutil "github.com/kubewharf/katalyst-core/pkg/util/qos"
)

const socketPodInstanceModelKey = "instance-model"

// PodGrouper determines QoS related properties of the pod in admitting request
type PodGrouper struct {
	poolToSharedSubgroup  map[string]string
	defaultSharedSubgroup string
}

func IsDecdicatedCoresNumaExclusive(qosLevel string, annotations map[string]string) bool {
	if apiconsts.PodAnnotationQoSLevelDedicatedCores != qosLevel {
		return false
	}
	return qosutil.AnnotationsIndicateNUMAExclusive(annotations)
}

func (p *PodGrouper) IsShared30(qosLevel string, annotations map[string]string) bool {
	if subgroup, err := p.GetQoSGroup(qosLevel, annotations); err == nil {
		return subgroup == "shared-30"
	}

	return false
}

func identifyCPUSetPool(annoInReq map[string]string) string {
	if pool, ok := annoInReq[apiconsts.PodAnnotationCPUEnhancementCPUSet]; ok {
		return pool
	}

	// fall back to original composite (not flattened) form
	enhancementValue, ok := annoInReq[apiconsts.PodAnnotationCPUEnhancementKey]
	if !ok {
		return ""
	}

	flattenedEnhancements := map[string]string{}
	err := json.Unmarshal([]byte(enhancementValue), &flattenedEnhancements)
	if err != nil {
		return ""
	}
	return identifyCPUSetPool(flattenedEnhancements)
}

// GetQoSGroup returns qos group based on inputs of qos level and relevant annotation, e.g.
// input "dedicated_cores", ...                   => "dedicated"
// input "shared_cores", {"cpuset_pool": "batch"} => "shared-30" // should there be a valid cpuset pool mapping
func (p *PodGrouper) GetQoSGroup(qosLevel string, annotations map[string]string) (string, error) {
	switch qosLevel {
	case apiconsts.PodAnnotationQoSLevelDedicatedCores:
		return string(task.QoSGroupDedicated), nil
	case apiconsts.PodAnnotationQoSLevelSystemCores:
		return string(task.QoSGroupSystem), nil
	case apiconsts.PodAnnotationQoSLevelReclaimedCores:
		return string(task.QoSGroupReclaimed), nil
	}

	if qosLevel != apiconsts.PodAnnotationQoSLevelSharedCores {
		return "", fmt.Errorf("unrecognized qos level %s", qosLevel)
	}

	pool := identifyCPUSetPool(annotations)
	if v, ok := p.poolToSharedSubgroup[pool]; ok {
		return v, nil
	}

	return p.defaultSharedSubgroup, nil
}

func NewPodGrouper(poolToSharedSubgroup map[string]string, defaultSharedSubgroup string) *PodGrouper {
	return &PodGrouper{
		poolToSharedSubgroup:  poolToSharedSubgroup,
		defaultSharedSubgroup: defaultSharedSubgroup,
	}
}
