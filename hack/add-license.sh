#!/usr/bin/env bash

# Copyright 2022 The Katalyst Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

PROJECT=$(cd $(dirname $0)/..; pwd)

LICENSEHEADERCHECKER_VERSION=v1.4.0

GOBIN=${PROJECT}/bin go install github.com/lluissm/license-header-checker/cmd/license-header-checker@${LICENSEHEADERCHECKER_VERSION}

LICENSEIGNORE=$(cat ${PROJECT}/.licenseignore | tr '\n' ',')

# New files get the current year (override with LICENSE_YEAR). Existing
# headers are never rewritten: a file keeps the year of its creation, and
# ranges such as "2022-2026" stay untouched. This matches the common OSS
# convention (addlicense/kubebuilder: creation-year headers; Kubernetes:
# per-file creation years coexist in one repo). The old behavior passed -r
# with a fixed 2022 boilerplate, which rewrote every file back to 2022.
LICENSE_YEAR=${LICENSE_YEAR:-$(date +%Y)}
TMP_BOILERPLATE=$(mktemp ${TMPDIR:-/tmp}/boilerplate.XXXXXX.go.txt)
trap 'rm -f ${TMP_BOILERPLATE}' EXIT
sed "s/Copyright [0-9]\{4\} The Katalyst Authors\./Copyright ${LICENSE_YEAR} The Katalyst Authors./" \
    ${PROJECT}/hack/boilerplate.go.txt > ${TMP_BOILERPLATE}

# -a: add a header only when the file has none. -r is intentionally NOT
# passed so existing years/holders are preserved instead of being reset.
${PROJECT}/bin/license-header-checker -a -v -i ${LICENSEIGNORE} ${TMP_BOILERPLATE} . go
