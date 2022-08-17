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

build_targets() {
    local goflags goldflags gcflags
    goldflags="${GOLDFLAGS:-}"
    gcflags="${GOGCFLAGS:-}"
    goflags=${GOFLAGS:-}

    local arg
    local archs=()

    for arg; do
        if [[ "$arg" == -* ]]; then
            # args starting with a dash are flags for go.
            goflags+=("$arg")
        else
            targets+=("$arg")
        fi
    done

    # by default build for linux/amd64
    # does not support multi-arch for now
    if [[ -z ${TARGET_ARCHS} ]]; then
        archs=(amd64)
    else
        IFS=',' read -r -a archs <<< "${TARGET_ARCHS}"
    fi

    # target_bin_dir contains the GOOS and GOARCH
    # eg: ./output/bin/linux/amd64/
    for arch in ${archs[@]}; do
        export GOARCH=${arch}
        export GOOS=linux
        local target_bin_dir=$BIN_DIR/${GOOS}/${GOARCH}

        rm -rf $target_bin_dir
        mkdir -p $target_bin_dir

        if [[ ${#targets[*]} == 0 ]]; then
            targets=(katalyst-agent katalyst-controller katalyst-metric katalyst-scheduler katalyst-webhook)
        fi

        for target in "${targets[@]}"; do
            echo "Building $target for linux/${arch}"
            go build -o $target_bin_dir/$target \
                -ldflags "${goldflags:-}" \
                -gcflags "${gcflags:-}" \
                $goflags $ROOT_DIR/cmd/${target}
        done
    done

    if [[ "${BUILD_IMAGES}" =~ [yY] ]]; then
        local platform_list=("${archs[@]/#/linux/}")
        local build_platforms=$(IFS=,; echo "${platform_list[*]}")
        local builder=$(docker buildx create --use)
        for target in "${targets[@]}"; do
            echo "Building image for $target"
            docker buildx build --no-cache --load --platform ${build_platforms} --build-arg BINARY=${target} -f ${DOCKERFILE} -t ${REGISTRY}/${REGISTRY_NAMESPACE}/${target}:${IMAGE_TAG} ${ROOT_DIR}
        done
        docker buildx rm ${builder}
    fi
}
