package qosgroup

import "k8s.io/apimachinery/pkg/util/sets"

type QoSGroupInfoGetter interface {
	GetAssignedNumaNodes() sets.Int
}
