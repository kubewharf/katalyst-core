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

package node

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/robfig/cron/v3"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/overcommit/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

const (
	cronOvercommitFail    = "cronOvercommitFail"
	cronOvercommitSuccess = "cronOvercommitSuccess"
)

func (nc *NodeOvercommitController) cronWorker() {
	nocList, err := nc.nodeOvercommitLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("list noc fail: %v", err)
		_ = nc.metricsEmitter.StoreInt64(cronOvercommitFail, 1, metrics.MetricTypeNameCount)
		return
	}

	for _, noc := range nocList {
		err = nc.syncTimeBounds(noc)
		if err != nil {
			klog.Errorf("noc %v syncTimeBounds fail: %v", noc.Name, err)
			_ = nc.metricsEmitter.StoreInt64(cronOvercommitFail, 1, metrics.MetricTypeNameCount, metrics.MetricTag{Key: "configName", Val: noc.Name})
			continue
		}
	}

	_ = nc.metricsEmitter.StoreInt64(cronOvercommitSuccess, 1, metrics.MetricTypeNameCount)
}

func (nc *NodeOvercommitController) syncTimeBounds(noc *v1alpha1.NodeOvercommitConfig) error {
	if len(noc.Spec.TimeBounds) == 0 {
		return nil
	}

	var (
		now                     = time.Now()
		resourceOvercommitRatio = make(map[v1.ResourceName]string)
		resourceLastMissedRun   = map[v1.ResourceName]time.Time{}
	)

	for _, tb := range noc.Spec.TimeBounds {
		if !timeBoundValid(now, tb) {
			continue
		}

		for _, bound := range tb.Bounds {
			missedRun, _, err := getNextSchedule(bound, now, lastScheduleTime(noc), noc.Spec.StartingDeadlineSeconds)
			if err != nil {
				klog.Error(err)
				continue
			}
			if missedRun.IsZero() {
				continue
			}

			for resource, overcommitRatioStr := range bound.ResourceOvercommitRatio {
				if resource != v1.ResourceCPU && resource != v1.ResourceMemory {
					klog.Warningf("resource %v not support", resource)
					continue
				}

				klog.V(6).Infof("noc %v missedRun: %v, lastMissedRun: %v, now: %v", noc.Name, missedRun, resourceLastMissedRun[resource], now)
				lastMissedRun, ok := resourceLastMissedRun[resource]
				if !ok || missedRun.After(lastMissedRun) {
					if !missRunAllowed(noc, now, missedRun) {
						continue
					}

					resourceLastMissedRun[resource] = missedRun

					overcommitRatio, err := strconv.ParseFloat(overcommitRatioStr, 64)
					if err != nil {
						klog.Errorf("parse overcommit ratio %v fail: %v", overcommitRatioStr, err)
						continue
					}
					if overcommitRatio < 1.0 {
						klog.Errorf("overcommit ratio %v is less than 1, skip", overcommitRatioStr)
						continue
					}

					resourceOvercommitRatio[resource] = overcommitRatioStr
				}
			}
		}
	}

	// patch noc
	if len(resourceOvercommitRatio) > 0 {
		err := nc.patchNocResourceOvercommitRatio(noc, resourceOvercommitRatio)
		if err != nil {
			return err
		}
		err = nc.patchNocLastScheduleTime(noc)
		return err
	}
	return nil
}

func (nc *NodeOvercommitController) patchNocResourceOvercommitRatio(noc *v1alpha1.NodeOvercommitConfig, resourceOvercommitRatio map[v1.ResourceName]string) error {
	nocCopy := noc.DeepCopy()
	if nocCopy.Spec.ResourceOvercommitRatio == nil {
		nocCopy.Spec.ResourceOvercommitRatio = map[v1.ResourceName]string{}
	}
	for resource, value := range resourceOvercommitRatio {
		nocCopy.Spec.ResourceOvercommitRatio[resource] = value
	}

	timeout, cancel := context.WithTimeout(nc.ctx, time.Second)
	defer cancel()
	_, err := nc.nocUpdater.PatchNoc(timeout, noc, nocCopy)
	if err != nil {
		klog.Errorf("noc %v patchNocResourceOvercommitRatio fail: %v", noc.Name, err)
		return err
	}
	return nil
}

func (nc *NodeOvercommitController) patchNocLastScheduleTime(noc *v1alpha1.NodeOvercommitConfig) error {
	nocCopy := noc.DeepCopy()
	nocCopy.Status.LastScheduleTime = &metav1.Time{Time: time.Now()}

	timeout, cancel := context.WithTimeout(nc.ctx, time.Second)
	defer cancel()
	_, err := nc.nocUpdater.PatchNocStatus(timeout, noc, nocCopy)
	if err != nil {
		klog.Errorf("noc %v patchNocLastScheduleTime fail: %v", noc.Name, err)
		return err
	}
	return nil
}

func missRunAllowed(noc *v1alpha1.NodeOvercommitConfig, now, lastRunTime time.Time) bool {
	if noc.Spec.CronConsistency {
		return true
	}

	if now.Sub(lastRunTime) < time.Minute {
		return true
	}
	return false
}

func timeBoundValid(now time.Time, bound v1alpha1.TimeBound) bool {
	if bound.Start.IsZero() && bound.End.IsZero() {
		return true
	}

	if bound.Start.IsZero() {
		return now.Before(bound.End.Time)
	}
	if bound.End.IsZero() {
		return now.After(bound.Start.Time)
	}
	klog.V(6).Infof("timeBoundValid, now: %v, start: %v, end: %v", now, bound.Start.Time, bound.End.Time)
	return now.After(bound.Start.Time) && now.Before(bound.End.Time)
}

func getNextSchedule(bound v1alpha1.Bound, now time.Time, earliestTime time.Time, schedulingDeadlineSeconds *int64) (lastMissed time.Time, next time.Time, err error) {
	sched, err := cron.ParseStandard(bound.CronTab)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("ParseStandard fail, cronTab: %v, err: %v", bound.CronTab, err)
	}
	if schedulingDeadlineSeconds != nil {
		schedulingDeadline := now.Add(-time.Second * time.Duration(*schedulingDeadlineSeconds))
		if schedulingDeadline.After(earliestTime) {
			earliestTime = schedulingDeadline
		}
	}
	if earliestTime.After(now) {
		return time.Time{}, sched.Next(now), nil
	}

	starts := 0
	for t := sched.Next(earliestTime); !t.After(now); t = sched.Next(t) {
		lastMissed = t
		starts++
		if starts > 100 {
			return time.Time{}, time.Time{}, fmt.Errorf("too many missed jobs (> 100). Set or decrease .spec.startingDeadlineSeconds or check clock skew")
		}
	}
	return lastMissed, sched.Next(now), nil
}

func lastScheduleTime(noc *v1alpha1.NodeOvercommitConfig) time.Time {
	if noc.Status.LastScheduleTime != nil {
		return noc.Status.LastScheduleTime.Time
	}
	return noc.CreationTimestamp.Time
}
