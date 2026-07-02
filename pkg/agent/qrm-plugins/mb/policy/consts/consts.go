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

package consts

const (
	MBPluginPolicyNameGeneric = "generic"

	// BytesPerMB is 1,000,000 for mem bandwidth usage calculation, not 1024*1024
	BytesPerMB = 1000 * 1000

	// BytesPerMiB is 2-based 1024*1024, for internal stats  and metrics reported
	BytesPerMiB = 1024 * 1024
)
