package vpa

import (
	"context"
	"sync"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	autoscalelister "github.com/kubewharf/katalyst-api/pkg/client/listers/autoscaling/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	statusWorkerCount = 5
	statusQueueLen    = 500000
)

type vpaStatusManager struct {
	ctx context.Context

	statusQueue chan string
	statusMap   map[string]*vpaStatusEvent
	statusMtx   sync.Mutex

	vpaUpdater control.VPAUpdater
	vpaLister  autoscalelister.KatalystVerticalPodAutoscalerLister
}

// vpaStatusEvent stores the information of vpa status.
type vpaStatusEvent struct {
	namespace string
	name      string
	uid       types.UID
	status    *apis.KatalystVerticalPodAutoscalerStatus
}

func newVPAStatusManager(ctx context.Context, vpaLister autoscalelister.KatalystVerticalPodAutoscalerLister,
	vpaUpdater control.VPAUpdater) vpaStatusManager {
	return vpaStatusManager{
		ctx:         ctx,
		statusQueue: make(chan string, statusQueueLen),
		statusMap:   make(map[string]*vpaStatusEvent),
		vpaUpdater:  vpaUpdater,
		vpaLister:   vpaLister,
	}
}

func (vs *vpaStatusManager) run() {
	for i := 0; i < statusWorkerCount; i++ {
		go vs.updateVPAStatus()
	}
	<-vs.ctx.Done()
}

func (vs *vpaStatusManager) tryUpdateVPAStatus(vpa *apis.KatalystVerticalPodAutoscaler) {
	key := native.GenerateNamespaceNameKey(vpa.Namespace, vpa.Name)
	vs.addEventStatus(vpa, key)
}

func (vs *vpaStatusManager) updateVPAStatus() {
	for {
		select {
		case key, ok := <-vs.statusQueue:
			if !ok {
				klog.Infof("vpa statusQueue is closed")
				return
			}

			vpa := vs.getEventStatus(key)
			if vpa == nil {
				break
			}

			if _, err := vs.vpaUpdater.UpdateVPAStatus(context.TODO(), vpa, metav1.UpdateOptions{}); err != nil {
				klog.Errorf("failed to update vpa for %s: %v", vpa.Name, err)
			}
			klog.Infof("successfully updated vpa status %s to %+v", vpa.Name, vpa.Status)
		case <-vs.ctx.Done():
			klog.Infoln("stop vpa statusQueue worker.")
			return
		}
	}
}

// add vpa status into the queue.
func (vs *vpaStatusManager) addEventStatus(vpa *apis.KatalystVerticalPodAutoscaler, key string) {
	event := &vpaStatusEvent{
		namespace: vpa.Namespace,
		name:      vpa.Name,
		uid:       vpa.UID,
		status:    &vpa.Status,
	}

	vs.statusMtx.Lock()
	defer vs.statusMtx.Unlock()

	if _, ok := vs.statusMap[key]; !ok {
		vs.statusQueue <- key
	}
	// if previous vpa status hasn't been updated, we just ignore it and update to latest.
	vs.statusMap[key] = event
}

// get vpa status from the queue.
func (vs *vpaStatusManager) getEventStatus(key string) *apis.KatalystVerticalPodAutoscaler {
	vs.statusMtx.Lock()
	defer func() {
		delete(vs.statusMap, key)
		vs.statusMtx.Unlock()
	}()

	event, ok := vs.statusMap[key]
	if !ok {
		klog.Warningf("vpa doesn't exist for key: %s", key)
		return nil
	}

	vpa, err := vs.vpaLister.KatalystVerticalPodAutoscalers(event.namespace).Get(event.name)
	if err != nil {
		klog.Errorf("failed to get vpa with %s/%s: %c", event.namespace, event.name, err)
		return nil
	} else if vpa.UID != event.uid {
		klog.Errorf("vpa %s/%s uid changed from %v to %v", event.namespace, event.name, event.uid, vpa.UID)
		return nil
	}

	// skip a write if we wouldn't need to update
	if apiequality.Semantic.DeepEqual(vpa.Status, event.status) {
		klog.Infof("vpa %s/%s uid changed from %v to %v", event.namespace, event.name, event.uid, vpa.UID)
		return nil
	}

	vpa.Status = *event.status
	return vpa
}
