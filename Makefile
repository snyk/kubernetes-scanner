# © 2023 Snyk Limited
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
SHELL := /bin/bash

ifeq ($(CIRCLE_SHA1),)
	GIT_COMMIT := $(shell git rev-parse --verify --short HEAD)
else 
	GIT_COMMIT := $(CIRCLE_SHA1)
endif

ifeq ($(CIRCLE_TAG),)
	TAG := v0.0.0-$(GIT_COMMIT)
else
	TAG := $(CIRCLE_TAG)
endif


GOCMD=go
GOMOD=$(GOCMD) mod
GOBUILD=$(GOCMD) build
GOTEST=$(GOCMD) test
GOGENERATE=$(GOCMD) generate
DOCKER=docker

all: fmt lint tidy generate test build
	$(info  "completed running make file for golang project")
fmt:
	@go fmt ./...
lint:
	env GOROOT=$$(go env GOROOT) golangci-lint run ./...
tidy:
	$(GOMOD) tidy -v
generate:
	$(GOGENERATE) ./...
test:
	$(GOTEST) ./... 
build:
	$(GOBUILD) -v
image:
	$(DOCKER) build \
		-t gcr.io/snyk-main/kubernetes-scanner:$(TAG) \
		-t gcr.io/snyk-main/kubernetes-scanner:latest \
		--build-arg COMMIT_SHA='$(GIT_COMMIT)' \
		--build-arg GIT_TAG="${CIRCLE_TAG}" \
		.
image-push:
	$(DOCKER) push gcr.io/snyk-main/kubernetes-scanner:$(TAG)

chart:
	$(GOCMD) run ./build/helmreleaser -version=$(TAG) -publish=false

chart-push:
	$(GOCMD) run ./build/helmreleaser -version=$(TAG) -publish=true

release:
	npx \
		-p '@semantic-release/commit-analyzer' \
		-p 'conventional-changelog-conventionalcommits' \
		-p '@semantic-release/github' \
		semantic-release

