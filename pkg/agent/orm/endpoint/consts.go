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

package endpoint

import "time"

const (
	// errFailedToDialResourcePlugin is the error raised when the resource plugin could not be
	// reached on the registered socket
	errFailedToDialResourcePlugin = "failed to dial resource plugin:"
	// errEndpointStopped indicates that the endpoint has been stopped
	errEndpointStopped = "endpoint %v has been stopped"
)

// endpointStopGracePeriod indicates the grace period after an endpoint is stopped
// because its resource plugin fails. QoSResourceManager keeps the stopped endpoint in its
// cache during this grace period to cover the time gap for the capacity change to
// take effect.
const endpointStopGracePeriod = time.Duration(5) * time.Minute
