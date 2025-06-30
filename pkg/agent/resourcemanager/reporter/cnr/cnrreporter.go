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

package cnr

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	clientset "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned"
	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/resourcemanager/reporter"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaservercnr "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/cnr"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/syntax"
)

const (
	cnrReporterName = "newCNR-reporter"

	// cnrUpdateMaxRetryTimes update newCNR retry time.
	cnrUpdateMaxRetryTimes = 3
)

const (
	refreshLatestCNRJitterFactor = 0.5
)

const (
	metricsNameRefreshCNRCost            = "refresh_cnr_cost"
	metricsNameUpdateCNRCost             = "update_cnr_cost"
	metricsNameUpdateCNRSpecMetadataCost = "update_cnr_spec_metadata_cost"
	metricsNameUpdateCNRStatusCost       = "update_cnr_status_cost"
)

// cnrReporterImpl is to report newCNR content to remote
type cnrReporterImpl struct {
	cnrName string

	// defaultLabels contains the default config for CNR created by reporter
	defaultLabels map[string]string
	// latestUpdatedCNR is used as an in-memory cache for CNR;
	// whenever CNR info is needed, get from this cache firstly
	latestUpdatedCNR *nodev1alpha1.CustomNodeResource
	mux              sync.Mutex

	notifiers map[string]metaservercnr.CNRNotifier

	client  clientset.Interface
	updater control.CNRControl
	emitter metrics.MetricEmitter

	mergeValueFunc syntax.MergeValueFunc

	refreshLatestCNRPeriod time.Duration
}

// NewCNRReporter create a newCNR reporter
func NewCNRReporter(genericClient *client.GenericClientSet, metaServer *metaserver.MetaServer,
	emitter metrics.MetricEmitter, conf *config.Configuration,
) (reporter.Reporter, error) {
	c := &cnrReporterImpl{
		cnrName:                conf.NodeName,
		refreshLatestCNRPeriod: conf.RefreshLatestCNRPeriod,
		defaultLabels:          conf.DefaultCNRLabels,
		notifiers:              make(map[string]metaservercnr.CNRNotifier),
		emitter:                emitter,
		client:                 genericClient.InternalClient,
		updater:                control.NewCNRControlImpl(genericClient.InternalClient),
	}
	// register itself as a resource reporter in meta-server
	metaServer.SetCNRFetcher(c)

	c.mergeValueFunc = syntax.SimpleMergeTwoValues
	return c, nil
}

// Run start newCNR reporter
func (c *cnrReporterImpl) Run(ctx context.Context) {
	go wait.JitterUntilWithContext(ctx, c.refreshLatestCNR, c.refreshLatestCNRPeriod, refreshLatestCNRJitterFactor, true)
	<-ctx.Done()
}

// GetCNR tries to return local cache if exists, otherwise get from APIServer

func (c *cnrReporterImpl) GetCNR(ctx context.Context) (*nodev1alpha1.CustomNodeResource, error) {
	cnr := c.latestUpdatedCNR.DeepCopy()
	if cnr != nil {
		return cnr, nil
	}

	return c.client.NodeV1alpha1().CustomNodeResources().Get(ctx, c.cnrName, metav1.GetOptions{ResourceVersion: "0"})
}

// Update is to update remote newCNR according to reported fields
func (c *cnrReporterImpl) Update(ctx context.Context, fields []*v1alpha1.ReportField) error {
	beginWithLock := time.Now()
	c.mux.Lock()
	beginWithoutLock := time.Now()

	defer func() {
		costs := time.Since(beginWithoutLock)
		klog.InfoS("finished update newCNR without lock", "costs", costs)

		c.mux.Unlock()

		costs = time.Since(beginWithLock)
		klog.InfoS("finished update newCNR with lock", "costs", costs)
		_ = c.emitter.StoreInt64(metricsNameUpdateCNRCost, costs.Microseconds(), metrics.MetricTypeNameRaw)
	}()

	if klog.V(4).Enabled() {
		for _, f := range fields {
			klog.Infof("field name %s/%s with value %s", f.FieldType, f.FieldName, string(f.Value))
		}
	}

	for i := 0; i < cnrUpdateMaxRetryTimes; i++ {
		if err := c.tryUpdateCNR(ctx, fields, i); err != nil {
			klog.Errorf("error updating newCNR, will retry: %v", err)
		} else {
			return nil
		}
	}

	return fmt.Errorf("attempt to update newCNR failed with total retries of %d", cnrUpdateMaxRetryTimes)
}

// RegisterNotifier register a notifier to newCNR reporter
func (c *cnrReporterImpl) RegisterNotifier(name string, notifier metaservercnr.CNRNotifier) error {
	c.mux.Lock()
	defer c.mux.Unlock()

	if _, ok := c.notifiers[name]; ok {
		return fmt.Errorf("notifier %s already exists", name)
	}

	c.notifiers[name] = notifier
	return nil
}

// UnregisterNotifier unregister a notifier from newCNR reporter
func (c *cnrReporterImpl) UnregisterNotifier(name string) error {
	c.mux.Lock()
	defer c.mux.Unlock()

	if _, ok := c.notifiers[name]; !ok {
		return fmt.Errorf("notifier %s not exists", name)
	}

	delete(c.notifiers, name)
	return nil
}

// refreshLatestCNR get latest newCNR from remote, because newCNR in cache may not have been updated.
func (c *cnrReporterImpl) refreshLatestCNR(ctx context.Context) {
	c.mux.Lock()
	defer c.mux.Unlock()

	begin := time.Now()
	defer func() {
		costs := time.Since(begin)
		klog.Infof("finished refresh newCNR (%v)", costs)
		_ = c.emitter.StoreInt64(metricsNameRefreshCNRCost, costs.Microseconds(), metrics.MetricTypeNameRaw)
	}()

	cnr, err := c.client.NodeV1alpha1().CustomNodeResources().Get(ctx, c.cnrName, metav1.GetOptions{ResourceVersion: "0"})
	if err == nil {
		c.latestUpdatedCNR = cnr.DeepCopy()
	} else if !c.resetCNRIfNeeded(err) {
		klog.Errorf("refresh local newCNR cache failed with error: %v", err)
	}
}

// tryUpdateCNR update newCNR according reported fields, first update newCNR try will use cached latestUpdatedCNR,
// if there are some errors such as conflict happened, it will retry by getting newCNR from api server
func (c *cnrReporterImpl) tryUpdateCNR(ctx context.Context, fields []*v1alpha1.ReportField, tryIdx int) error {
	var (
		cnr *nodev1alpha1.CustomNodeResource
		err error
	)

	// only get newCNR from api server iff latest updated newCNR is nil or tryIdx > 0
	if c.latestUpdatedCNR == nil || tryIdx > 0 {
		c.countMetricsWithBaseTags("reporter_update_retry")

		cnr, err = c.client.NodeV1alpha1().CustomNodeResources().Get(ctx, c.cnrName, metav1.GetOptions{ResourceVersion: "0"})
		if err != nil && !apierrors.IsNotFound(err) {
			c.countMetricsWithBaseTags("reporter_update_get_failed")
			if c.resetCNRIfNeeded(err) {
				return nil
			}
			return err
		}

		// NotFound to create newCNR
		if err != nil {
			cnr, err = c.createCNR(ctx, fields)
			if err != nil {
				c.countMetricsWithBaseTags("reporter_update_failed")
				return fmt.Errorf("create newCNR failed: %s", err)
			}
		}

		c.latestUpdatedCNR = cnr.DeepCopy()
	} else {
		cnr = c.latestUpdatedCNR.DeepCopy()
	}

	if cnr == nil {
		return fmt.Errorf("nil %q newCNR object", c.cnrName)
	}

	originCNR := cnr.DeepCopy()
	err = setCNR(originCNR, cnr, fields, c.mergeValueFunc)
	if err != nil {
		return err
	}

	// todo: consider whether we need to handle update error automatically
	//  i.e. use queue to push and pop those failed items

	// try patch spec and metadata first, because the update of newCNR will change the ResourceVersion in ObjectMeta
	originCNR, err = c.tryUpdateCNRSpecAndMetadata(ctx, originCNR, cnr)
	if err != nil && !c.resetCNRIfNeeded(err) {
		return err
	} else if err != nil {
		originCNR = c.latestUpdatedCNR.DeepCopy()
	}

	_, err = c.tryUpdateCNRStatus(ctx, originCNR, cnr)
	if err != nil {
		return err
	}

	return nil
}

func (c *cnrReporterImpl) tryUpdateCNRSpecAndMetadata(ctx context.Context,
	originCNR, currentCNR *nodev1alpha1.CustomNodeResource,
) (*nodev1alpha1.CustomNodeResource, error) {
	var (
		cnr *nodev1alpha1.CustomNodeResource
		err error
	)

	if cnrSpecHasChanged(&originCNR.Spec, &currentCNR.Spec) || cnrMetadataHasChanged(&originCNR.ObjectMeta, &currentCNR.ObjectMeta) {
		klog.Infof("newCNR spec or metadata changed, try to patch it")

		begin := time.Now()
		defer func() {
			costs := time.Since(begin)
			klog.Infof("finished update newCNR spec and metadata (%v)", costs)
			_ = c.emitter.StoreInt64(metricsNameUpdateCNRSpecMetadataCost, costs.Microseconds(), metrics.MetricTypeNameRaw)
		}()

		// patch newCNR spec and metadata
		cnr, err = c.updater.PatchCNRSpecAndMetadata(ctx, c.cnrName, originCNR, currentCNR)
		if err != nil {
			c.countMetricsWithBaseTags("reporter_update",
				metrics.ConvertMapToTags(map[string]string{
					"field":  "spec",
					"status": "failed",
				})...)
			return nil, err
		}

		c.countMetricsWithBaseTags("reporter_update",
			metrics.ConvertMapToTags(map[string]string{
				"field":  "spec",
				"status": "success",
			})...)

		klog.Infof("patch newCNR spec and metadata success\n old newCNR spec: %#v, metadata: %#v,\n "+
			"new newCNR spec: %#v, metadata: %#v",
			originCNR.Spec, originCNR.ObjectMeta, cnr.Spec, cnr.ObjectMeta)
		c.latestUpdatedCNR = cnr.DeepCopy()

		// notify newCNR spec and metadata update
		for _, notifier := range c.notifiers {
			notifier.OnCNRUpdate(cnr)
		}
	} else {
		return originCNR, nil
	}

	return cnr, nil
}

func (c *cnrReporterImpl) tryUpdateCNRStatus(ctx context.Context,
	originCNR, currentCNR *nodev1alpha1.CustomNodeResource,
) (*nodev1alpha1.CustomNodeResource, error) {
	var (
		cnr *nodev1alpha1.CustomNodeResource
		err error
	)

	if cnrStatusHasChanged(&originCNR.Status, &currentCNR.Status) {
		klog.Infof("newCNR status changed, try to patch it")

		begin := time.Now()
		defer func() {
			costs := time.Since(begin)
			klog.Infof("finished update newCNR status (%v)", costs)
			_ = c.emitter.StoreInt64(metricsNameUpdateCNRStatusCost, costs.Microseconds(), metrics.MetricTypeNameRaw)
		}()

		// patch newCNR status
		cnr, err = c.updater.PatchCNRStatus(ctx, c.cnrName, originCNR, currentCNR)
		if err != nil {
			c.countMetricsWithBaseTags("reporter_update",
				metrics.ConvertMapToTags(map[string]string{
					"field":  "status",
					"status": "failed",
				})...)
			return nil, err
		}

		c.countMetricsWithBaseTags("reporter_update",
			metrics.ConvertMapToTags(map[string]string{
				"field":  "status",
				"status": "success",
			})...)

		if klog.V(6).Enabled() {
			klog.Infof("patch newCNR status success old status: %#v,\n new status: %#v", originCNR.Status, cnr.Status)
		}

		c.latestUpdatedCNR = cnr.DeepCopy()

		// notify newCNR status update
		for _, notifier := range c.notifiers {
			notifier.OnCNRStatusUpdate(cnr)
		}
	} else {
		return originCNR, nil
	}

	return cnr, nil
}

// resetCNRIfNeeded reset newCNR if unmarshal type error, it will initialize
// local newCNR cache to make sure the content of newCNR always is true
// todo if $ref is supported in CRD, we can skip this since api-server will help with validations
func (c *cnrReporterImpl) resetCNRIfNeeded(err error) bool {
	if general.IsUnmarshalTypeError(err) {
		c.latestUpdatedCNR = c.defaultCNR()
		klog.Infof("success re-initialize local newCNR cache")
		return true
	}

	return false
}

func (c *cnrReporterImpl) defaultCNR() *nodev1alpha1.CustomNodeResource {
	return &nodev1alpha1.CustomNodeResource{
		ObjectMeta: metav1.ObjectMeta{
			Name:   c.cnrName,
			Labels: c.defaultLabels,
		},
	}
}

func (c *cnrReporterImpl) createCNR(ctx context.Context, fields []*v1alpha1.ReportField) (*nodev1alpha1.CustomNodeResource, error) {
	cnr := c.defaultCNR()

	err := setCNR(nil, cnr, fields, c.mergeValueFunc)
	if err != nil {
		return nil, fmt.Errorf("set newCNR failed: %s", err)
	}

	klog.Infof("try to create newCNR: %#v", cnr)

	cnr, err = c.client.NodeV1alpha1().CustomNodeResources().Create(ctx, cnr, metav1.CreateOptions{})
	if err != nil {
		return cnr, err
	}

	return cnr, nil
}

func setCNR(originCNR, newCNR *nodev1alpha1.CustomNodeResource, fields []*v1alpha1.ReportField,
	mergeFunc func(src reflect.Value, dst reflect.Value) error,
) error {
	var errList []error
	initializedFields := sets.String{}
	for _, f := range fields {
		if f == nil {
			continue
		}

		// initialize need report newCNR field first
		if !initializedFields.Has(f.FieldName) {
			err := initializeFieldToCNR(newCNR, *f)
			if err != nil {
				errList = append(errList, err)
				continue
			}

			initializedFields.Insert(f.FieldName)
		}

		// parse report field to newCNR by merge function
		_, err := parseReportFieldToCNR(newCNR, *f, mergeFunc)
		if err != nil {
			errList = append(errList, err)
			continue
		}
	}

	if len(errList) > 0 {
		return errors.NewAggregate(errList)
	}

	if err := reviseCNR(originCNR, newCNR); err != nil {
		return err
	}

	return nil
}

// reviseCNR revises the field of newCNR by origin newCNR to make sure it is not redundant and
// support merge operation
func reviseCNR(originCNR, newCNR *nodev1alpha1.CustomNodeResource) error {
	if newCNR == nil {
		return nil
	}

	// merge all topology zones
	newCNR.Status.TopologyZone = util.MergeTopologyZone(nil, newCNR.Status.TopologyZone)

	// merge taints that reporter does not manage with taints that reporter manages
	var notManagedTaints []nodev1alpha1.Taint
	if originCNR != nil {
		notManagedTaints = util.CNRTaintSetFilter(originCNR.Spec.Taints, util.IsNotManagedByReporterCNRTaint)
	}
	newCNR.Spec.Taints = util.MergeTaints(notManagedTaints, newCNR.Spec.Taints)
	return nil
}

func (c *cnrReporterImpl) countMetricsWithBaseTags(key string, tags ...metrics.MetricTag) {
	tags = append(tags,
		metrics.ConvertMapToTags(map[string]string{
			"reporterName": cnrReporterName,
		})...)

	_ = c.emitter.StoreInt64(key, 1, metrics.MetricTypeNameCount, tags...)
}

// initializeFieldToCNR initialize newCNR fields to nil
func initializeFieldToCNR(cnr *nodev1alpha1.CustomNodeResource, field v1alpha1.ReportField) error {
	// get need report value of newCNR
	originValue, err := getCNRField(cnr, field)
	if err != nil {
		return err
	}

	originValue.Set(reflect.New(originValue.Type()).Elem())
	return nil
}

// parseReportFieldToCNR parse reportField and merge to origin newCNR by mergeFunc
func parseReportFieldToCNR(cnr *nodev1alpha1.CustomNodeResource, reportField v1alpha1.ReportField,
	mergeFunc func(src reflect.Value, dst reflect.Value) error,
) (*nodev1alpha1.CustomNodeResource, error) {
	if cnr == nil {
		return nil, fmt.Errorf("newCNR is nil")
	}

	// get need report value of newCNR
	originValue, err := getCNRField(cnr, reportField)
	if err != nil {
		return nil, err
	}

	// parse report value to base field type
	reportValue, err := syntax.ParseBytesByType(reportField.Value, originValue.Type())
	if err != nil || !reportValue.IsValid() {
		return nil, fmt.Errorf("report %s with value %s is invald with err: %s", reportField.FieldName, string(reportField.Value), err)
	}

	err = mergeFunc(reportValue, originValue)
	if err != nil {
		return nil, err
	}

	return cnr, nil
}

// getCNRField only support to parse first-level fields in newCNR now;
// todo: support to parse nested fields in the future.
func getCNRField(cnr *nodev1alpha1.CustomNodeResource, reportField v1alpha1.ReportField) (reflect.Value, error) {
	var el reflect.Value
	switch reportField.FieldType {
	case v1alpha1.FieldType_Status:
		el = reflect.ValueOf(&cnr.Status)
	case v1alpha1.FieldType_Spec:
		el = reflect.ValueOf(&cnr.Spec)
	case v1alpha1.FieldType_Metadata:
		el = reflect.ValueOf(cnr)
	default:
		return reflect.Value{}, fmt.Errorf("not support field type %s", reportField.FieldType)
	}

	if el.Kind() == reflect.Ptr {
		el = el.Elem()
	}

	// find origin value by field name
	field := el.FieldByName(reportField.FieldName)
	if !field.IsValid() {
		return reflect.Value{}, fmt.Errorf("field %s is invalid", reportField.FieldName)
	}

	return field, nil
}

func cnrMetadataHasChanged(originMeta *metav1.ObjectMeta, meta *metav1.ObjectMeta) bool {
	return !apiequality.Semantic.DeepEqual(originMeta, meta)
}

func cnrSpecHasChanged(originSpec *nodev1alpha1.CustomNodeResourceSpec, spec *nodev1alpha1.CustomNodeResourceSpec) bool {
	return !apiequality.Semantic.DeepEqual(originSpec, spec)
}

func cnrStatusHasChanged(originStatus *nodev1alpha1.CustomNodeResourceStatus, status *nodev1alpha1.CustomNodeResourceStatus) bool {
	return !apiequality.Semantic.DeepEqual(originStatus, status)
}
