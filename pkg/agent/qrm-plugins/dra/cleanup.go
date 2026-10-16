/*
Copyright 2026 The Katalyst Authors.

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

package dra

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/dra/state"
)

const (
	stateCheckPeriod = 30 * time.Second
	maxResidualTime  = 5 * time.Minute
)

func (p *StaticPolicy) cleanupResidualClaims() error {
	p.cleanupMu.Lock()
	defer p.cleanupMu.Unlock()

	ctx := p.runContext
	if ctx == nil {
		ctx = context.Background()
	}
	preparedClaims := p.state.ListPreparedClaims()
	residual, observeErr := p.observeResidualClaims(ctx, preparedClaims)
	cleanupErr := p.cleanupConfirmedResidualClaims(ctx, preparedClaims, residual)
	return errors.Join(observeErr, cleanupErr)
}

func (p *StaticPolicy) observeResidualClaims(
	ctx context.Context,
	preparedClaims map[string]state.PreparedClaim,
) (sets.String, error) {
	residual := sets.NewString()
	var result error
	for uid, prepared := range preparedClaims {
		isResidual, err := p.isResidualClaim(ctx, prepared)
		if err != nil {
			result = errors.Join(result, fmt.Errorf(
				"get ResourceClaim %s/%s: %w",
				prepared.Namespace,
				prepared.Name,
				err,
			))
			continue
		}
		if isResidual {
			residual.Insert(uid)
			p.residualHitMap[uid]++
			continue
		}
		delete(p.residualHitMap, uid)
	}
	return residual, result
}

func (p *StaticPolicy) isResidualClaim(
	ctx context.Context,
	prepared state.PreparedClaim,
) (bool, error) {
	claim, err := p.kubeClient.ResourceV1().
		ResourceClaims(prepared.Namespace).
		Get(ctx, prepared.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return string(claim.UID) != prepared.UID, nil
}

// cleanupConfirmedResidualClaims unprepares claims that remain residual for the configured grace period.
func (p *StaticPolicy) cleanupConfirmedResidualClaims(
	ctx context.Context,
	preparedClaims map[string]state.PreparedClaim,
	residual sets.String,
) error {
	var result error
	for uid, hitCount := range p.residualHitMap {
		prepared, stillPrepared := preparedClaims[uid]
		if !stillPrepared {
			delete(p.residualHitMap, uid)
			continue
		}
		if !residual.Has(uid) ||
			time.Duration(hitCount)*stateCheckPeriod < maxResidualTime {
			continue
		}

		err := p.unprepareOne(
			ctx,
			claimRefValue(prepared.Namespace, prepared.Name, uid),
		)
		if err != nil {
			result = errors.Join(result, fmt.Errorf(
				"unprepare residual ResourceClaim %s/%s (%s): %w",
				prepared.Namespace,
				prepared.Name,
				uid,
				err,
			))
			continue
		}
		delete(p.residualHitMap, uid)
	}
	return result
}
