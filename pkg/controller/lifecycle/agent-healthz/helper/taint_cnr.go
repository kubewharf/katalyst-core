/*
Copyright 2022 The Katalyst Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package helper

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/controller/nodelifecycle/scheduler"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	listers "github.com/kubewharf/katalyst-api/pkg/client/listers/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	metricsNameUntaintedCNRCount = "untainted_cnr_count"
	metricsNameTaintedCNRCount   = "tainted_cnr_count"
)

const TaintNameNoScheduler = "TaintNameNoScheduler"

var TaintNoScheduler = &apis.Taint{
	Key:    corev1.TaintNodeUnschedulable,
	Effect: apis.TaintEffectNoScheduleForReclaimedTasks,
}

var allTaints = []*apis.Taint{
	TaintNoScheduler,
}

// CNRTaintItem records the detailed item to perform cnr-taints
type CNRTaintItem struct {
	Taints map[string]*apis.Taint
}

type CNRTaintHelper struct {
	ctx        context.Context
	emitter    metrics.MetricEmitter
	cnrControl control.CNRControl

	checker *HealthzHelper
	queue   *scheduler.RateLimitedTimedQueue

	nodeLister corelisters.NodeLister
	cnrLister  listers.CustomNodeResourceLister
}

// NewTaintHelper todo add logic here
func NewTaintHelper(ctx context.Context, emitter metrics.MetricEmitter, cnrControl control.CNRControl,
	nodeLister corelisters.NodeLister, cnrLister listers.CustomNodeResourceLister,
	queue *scheduler.RateLimitedTimedQueue, checker *HealthzHelper,
) *CNRTaintHelper {
	return &CNRTaintHelper{
		ctx:        ctx,
		emitter:    emitter,
		cnrControl: cnrControl,

		checker: checker,
		queue:   queue,

		nodeLister: nodeLister,
		cnrLister:  cnrLister,
	}
}

func (t *CNRTaintHelper) Run() {
	go wait.Until(t.doTaint, scheduler.NodeEvictionPeriod, t.ctx.Done())
}

// doTaint is used to pop nodes from to-be-tainted queue,
// and then trigger the taint actions
func (t *CNRTaintHelper) doTaint() {
	t.queue.Try(func(value scheduler.TimedValue) (bool, time.Duration) {
		node := value.Value

		cnr, err := t.cnrLister.Get(value.Value)
		if errors.IsNotFound(err) {
			klog.Warningf("cnr %v no longer present in cnrLister", value.Value)
			return true, 0
		} else if err != nil {
			klog.Errorf("cannot find cnr for node %v err %v", node, err)
			// retry in 50 millisecond
			return false, 50 * time.Millisecond
		}

		// second confirm that we should taint cnr
		item := value.UID.(*CNRTaintItem)
		needTaint := t.checker.CheckAllAgentReady(node)
		if needTaint && len(item.Taints) != 0 {
			if err := t.taintCNR(cnr, item); err != nil {
				klog.Warningf("failed to taint for cnr %v: %v", value.Value, err)
				return false, 0
			}
		}

		return true, 0
	})
}

func (t *CNRTaintHelper) taintCNR(cnr *apis.CustomNodeResource, item *CNRTaintItem) error {
	var err error
	var newCNR *apis.CustomNodeResource
	for _, taint := range item.Taints {
		newCNR, _, err = util.AddOrUpdateCNRTaint(cnr, taint)
		if err != nil {
			return err
		}
	}

	if equality.Semantic.DeepEqual(cnr, newCNR) {
		general.Infof("taint already exits, not need to update")
		return nil
	}

	_, err = t.cnrControl.PatchCNRSpecAndMetadata(t.ctx, cnr.Name, cnr, newCNR)
	if err != nil {
		_ = t.emitter.StoreInt64(metricsNameTaintedCNRCount, 1, metrics.MetricTypeNameCount,
			[]metrics.MetricTag{
				{Key: "status", Val: "failed"},
				{Key: "name", Val: cnr.Name},
			}...)
		return err
	}
	_ = t.emitter.StoreInt64(metricsNameTaintedCNRCount, 1, metrics.MetricTypeNameCount,
		[]metrics.MetricTag{
			{Key: "status", Val: "success"},
			{Key: "name", Val: cnr.Name},
		}...)

	return nil
}

// TryUNTaintCNR is used to delete taint info from CNR
func (t *CNRTaintHelper) TryUNTaintCNR(name string) error {
	cnr, err := t.cnrLister.Get(name)
	if errors.IsNotFound(err) {
		klog.Warningf("cnr %v no longer present in cnrLister", name)
		return nil
	} else if err != nil {
		return err
	}

	var newCNR *apis.CustomNodeResource
	for _, taint := range allTaints {
		newCNR, _, err = util.RemoveCNRTaint(cnr, taint)
		if err != nil {
			return err
		}
	}

	if equality.Semantic.DeepEqual(cnr, newCNR) {
		klog.V(5).InfoS("taint already disappears, not need to update", "cnr", cnr.Name)
		return nil
	}

	_, err = t.cnrControl.PatchCNRSpecAndMetadata(t.ctx, cnr.Name, cnr, newCNR)
	if err != nil {
		_ = t.emitter.StoreInt64(metricsNameUntaintedCNRCount, 1, metrics.MetricTypeNameCount,
			[]metrics.MetricTag{
				{Key: "status", Val: "failed"},
				{Key: "name", Val: cnr.Name},
			}...)
		return err
	}
	_ = t.emitter.StoreInt64(metricsNameUntaintedCNRCount, 1, metrics.MetricTypeNameCount,
		[]metrics.MetricTag{
			{Key: "status", Val: "success"},
			{Key: "name", Val: cnr.Name},
		}...)

	return nil
}
