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

package cni

import (
	"context"
	"errors"
)

// CNIInfo contains the node network information given by CNI.
type CNIInfo struct {
	AllocatableDirectIPs int64
}

// CNIFetcher reads authoritative node network information from CNI.
// Implementations must return an error on context cancellation and must not subtract IP usage.
type CNIFetcher interface {
	FetchCNIInfo(ctx context.Context) (CNIInfo, error)
}

// Snapshot describes the latest completed fetch. Failed fetches have zero-valued
// Info; successful zero has a nil Err. Err does not describe Node publication.
// There is no last-success fallback or TTL.
type Snapshot struct {
	Info CNIInfo
	Err  error
}

func initialSnapshot() Snapshot {
	return Snapshot{Err: errors.New("CNI information not fetched yet")}
}
