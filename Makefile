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

GOPROXY := $(shell go env GOPROXY)
ifeq ($(GOPROXY),)
GOPROXY := https://proxy.golang.org
endif
export GOPROXY

MakeFilePath := $(shell dirname $(MAKEFILE_LIST))

GIT_COMMIT = $(shell git rev-parse HEAD)
ifeq ($(shell git tag --points-at ${GIT_COMMIT}),)
GIT_VERSION=$(shell echo ${GIT_COMMIT} | cut -c 1-7)
else
GIT_VERSION=$(shell git describe --abbrev=0 --tags --always)
endif

IMAGE_TAG = ${GIT_VERSION}
REGISTRY ?= docker.io
REGISTRY_NAMESPACE ?= kubewharf
REGISTRY_USER ?= ""
REGISTRY_PWD ?= ""

.EXPORT_ALL_VARIABLES:


all: generate

## --------------------------------------
## Generate / Manifests
## --------------------------------------

.PHONY: generate
generate:
	$(MAKE) generate-pb


## --------------------------------------
## Generate / Protocols
## --------------------------------------

.PHONY: generate-pb
generate-pb: generate-sys-advisor-cpu-plugin generate-advisor-svc generate-borwein-inference-svc

TempRepoDir := $(shell mktemp -d)
SysAdvisorCPUPluginPath = $(MakeFilePath)/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor/
.PHONY: generate-sys-advisor-cpu-plugin ## Generate protocol for cpu resource plugin with sys-advisor
generate-sys-advisor-cpu-plugin:
	mkdir -p $(TempRepoDir)/github.com/kubewharf && \
	mkdir -p $(TempRepoDir)/github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc && \
	mkdir -p $(TempRepoDir)/github.com/gogo && \
	git clone https://github.com/kubewharf/kubelet.git $(TempRepoDir)/github.com/kubewharf/kubelet && \
	git clone https://github.com/gogo/protobuf.git $(TempRepoDir)/github.com/gogo/protobuf && \
	cp -f $(MakeFilePath)/pkg/agent/qrm-plugins/advisorsvc/advisor_svc.proto $(TempRepoDir)/github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc/ && \
	targetTag=`cat $(MakeFilePath)/go.mod | grep kubewharf/kubelet | awk '{print $$4}'` && \
        cd $(TempRepoDir)/github.com/kubewharf/kubelet && \
		git fetch --tags && \
		git checkout $$targetTag && \
		cd - && \
		protoc -I=$(SysAdvisorCPUPluginPath) -I=$(TempRepoDir) --gogo_out=plugins=grpc,paths=source_relative:$(SysAdvisorCPUPluginPath) $(SysAdvisorCPUPluginPath)cpu.proto && \
	cat $(MakeFilePath)/hack/boilerplate.go.txt "$(SysAdvisorCPUPluginPath)cpu.pb.go" > tmpfile && mv tmpfile "$(SysAdvisorCPUPluginPath)cpu.pb.go"
	if [ `uname` == "Linux" ]; then sedi=(-i); else sedi=(-i ""); fi && \
		sed "$${sedi[@]}" s,github.com/kubewharf/kubelet,k8s.io/kubelet,g $(SysAdvisorCPUPluginPath)cpu.pb.go

AdvisorSvcPath = $(MakeFilePath)/pkg/agent/qrm-plugins/advisorsvc/
.PHONY: generate-advisor-svc ## Generate protocol for general qrm-plugin with sys-advisor
generate-advisor-svc:
	mkdir -p $(TempRepoDir)/github.com/kubewharf && \
	mkdir -p $(TempRepoDir)/github.com/gogo && \
	git clone https://github.com/kubewharf/kubelet.git $(TempRepoDir)/github.com/kubewharf/kubelet && \
	git clone https://github.com/gogo/protobuf.git $(TempRepoDir)/github.com/gogo/protobuf && \
	targetTag=`cat $(MakeFilePath)/go.mod | grep kubewharf/kubelet | awk '{print $$4}'` && \
        cd $(TempRepoDir)/github.com/kubewharf/kubelet && \
		git fetch --tags && \
		git checkout $$targetTag && \
		cd - && \
		protoc -I=$(AdvisorSvcPath) -I=$(TempRepoDir) --gogo_out=plugins=grpc,paths=source_relative:$(AdvisorSvcPath) $(AdvisorSvcPath)advisor_svc.proto && \
	cat $(MakeFilePath)/hack/boilerplate.go.txt "$(AdvisorSvcPath)advisor_svc.pb.go" > tmpfile && mv tmpfile "$(AdvisorSvcPath)advisor_svc.pb.go"
	if [ `uname` == "Linux" ]; then sedi=(-i); else sedi=(-i ""); fi && \
		sed "$${sedi[@]}" s,github.com/kubewharf/kubelet,k8s.io/kubelet,g $(AdvisorSvcPath)advisor_svc.pb.go

BorweinInferenceSvcPath = $(MakeFilePath)/pkg/agent/sysadvisor/plugin/inference/models/borwein/inferencesvc/
.PHONY: generate-borwein-inference-svc ## Generate protocol for borwein inference service
generate-borwein-inference-svc:
	protoc -I=$(BorweinInferenceSvcPath) -I=$(TempRepoDir) --gogo_out=plugins=grpc,paths=source_relative:$(BorweinInferenceSvcPath) $(BorweinInferenceSvcPath)inference_svc.proto && \
	cat $(MakeFilePath)/hack/boilerplate.go.txt "$(BorweinInferenceSvcPath)inference_svc.pb.go" > tmpfile && mv tmpfile "$(BorweinInferenceSvcPath)inference_svc.pb.go"


## --------------------------------------
## Cleanup / Verification
## --------------------------------------

.PHONY: clean
clean: ## Remove all generated files
	$(MAKE) clean-bin

.PHONY: clean-bin
clean-bin: ## Remove all generated binaries
	rm -rf bin
	rm -rf hack/tools/bin
	rm -rf output

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: fmt-strict
fmt-strict: ## Run go fmt against code.
	gofumpt -l -w .

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: ## Run go test against code.
	go test -v -json -coverprofile=coverage.txt -parallel=16 -p=16 -covermode=atomic -race -coverpkg=./... \
		`go list ./pkg/... | grep -E -v "pkg/scheduler|pkg/controller/resource-recommend|pkg/util/resource-recommend"`

.PHONY: license
license:
	./hack/add-license.sh


## --------------------------------------
## Build binaries and images
## --------------------------------------

# For now, only build binary and images for linux/amd64 platform by default
build-binaries:
	bash -c "build/build.sh ${TARGET}"

build-images: BUILD_IMAGES=y
build-images: build-binaries

controller:
	$(MAKE) build-binaries TARGET=katalyst-controller

agent:
	$(MAKE) build-binaries TARGET=katalyst-agent

webhook:
	$(MAKE) build-binaries TARGET=katalyst-webhook

scheduler:
	$(MAKE) build-binaries TARGET=katalyst-scheduler

metric:
	$(MAKE) build-binaries TARGET=katalyst-metric

all-binaries: controller agent webhook scheduler metric

image-controller:
	$(MAKE) build-images TARGET=katalyst-controller

image-agent:
	$(MAKE) build-images TARGET=katalyst-agent

image-webhook:
	$(MAKE) build-images TARGET=katalyst-webhook

image-scheduler:
	$(MAKE) build-images TARGET=katalyst-scheduler

image-metric:
	$(MAKE) build-images TARGET=katalyst-metric

all-images: image-controller image-agent image-webhook image-scheduler image-metric

docker-login:
ifneq ($(REGISTRY_USER), "")
	docker login -u $(REGISTRY_USER) -p $(REGISTRY_PWD) $(REGISTRY)
else
	@echo No login credential provided, skip log in...
endif

push-image-controller: docker-login image-controller
	docker push "${REGISTRY}/${REGISTRY_NAMESPACE}/katalyst-controller:${IMAGE_TAG}"

push-image-agent: docker-login image-agent
	docker push "${REGISTRY}/${REGISTRY_NAMESPACE}/katalyst-agent:${IMAGE_TAG}"

push-image-webhook: docker-login image-webhook
	docker push "${REGISTRY}/${REGISTRY_NAMESPACE}/katalyst-webhook:${IMAGE_TAG}"

push-image-scheduler: docker-login image-scheduler
	docker push "${REGISTRY}/${REGISTRY_NAMESPACE}/katalyst-scheduler:${IMAGE_TAG}"

push-image-metric: docker-login image-metric
	docker push "${REGISTRY}/${REGISTRY_NAMESPACE}/katalyst-metric:${IMAGE_TAG}"

push-all-images: docker-login push-image-controller push-image-agent push-image-webhook push-image-scheduler push-image-metric
