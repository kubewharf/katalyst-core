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

# todo
# once all code is public, enable multi-stage build
# FROM --platform=${BUILDPLATFORM} golang:1.18 as builder
# ADD . /build
# ARG TARGETOS TARGETARCH GIT_VERSION
# ARG BINARY
# WORKDIR /build/
# RUN  make build-linux TARGET=${BINARY}

FROM --platform=${BUILDPLATFORM} kubewharf/distroless-base-debian11:latest
ARG TARGETOS TARGETARCH
ARG BINARY
#COPY --from=builder /build/output/bin/${TARGETOS}/${TARGETARCH}/${BINARY} /usr/local/bin/${BINARY}
COPY ./output/bin/${TARGETOS}/${TARGETARCH}/${BINARY} /usr/local/bin/${BINARY}
