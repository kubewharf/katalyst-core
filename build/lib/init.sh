#!/bin/bash

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

TARGET_ARCHS=${TARGET_ARCHS:-""}

OUTPUT_DIR=${ROOT_DIR}/output
BIN_DIR=${OUTPUT_DIR}/bin
DOCKERFILE=${ROOT_DIR}/build/dockerfiles/Dockerfile.local

PROJECT_PREFIX=${PROJECT_PREFIX:-katalyst}
GIT_VERSION=${GIT_VERSION:-$(git describe --abbrev=0 --tags --always)}
GIT_COMMIT=$(git rev-parse HEAD)
BUILD_DATE=$(date -u +'%Y-%m-%dT%H:%M:%SZ')
BUILD_IMAGES=${BUILD_IMAGES:-n}

GO111MODULE=on

# to prepare C related dependencies
BUILD_HOST_OS=${BUILD_HOST_OS:-$(go env GOHOSTOS)}
[[ "${BUILD_HOST_OS}" == "linux" ]] && sudo apt install -y libpci-dev
[[ "${BUILD_HOST_OS}" != "linux" ]] && echo ${BUILD_HOST_OS} is not supported yet. Please use Linux for now || true
