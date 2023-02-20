SHELL := /bin/bash

ifeq ($(CIRCLE_SHA1),)
	GIT_COMMIT := $(shell git rev-parse --verify HEAD)
else 
	GIT_COMMIT := $(CIRCLE_SHA1)
endif

ifeq ($(CIRCLE_TAG),)
	TAG := $(GIT_COMMIT)
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
	$(DOCKER) build -t gcr.io/snyk-main/kubernetes-scanner:$(TAG) \
		--build-arg COMMIT_SHA='$(GIT_COMMIT)' \
		--build-arg GIT_TAG="${CIRCLE_TAG}" \
		.
push-image:
	$(DOCKER) push gcr.io/snyk-main/kubernetes-scanner:$(GIT_COMMIT)
