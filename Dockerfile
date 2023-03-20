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
FROM golang:1.20 as build

WORKDIR /go/src/app
COPY . .

ARG COMMIT_SHA
ARG GIT_TAG

RUN go mod download
RUN CGO_ENABLED=0 GOEXPERIMENT=boringcrypto go build \
    -ldflags="-s -w \
        -X github.com/snyk/kubernetes-scanner/build.commitSHA=$COMMIT_SHA \
        -X github.com/snyk/kubernetes-scanner/build.tag=$GIT_TAG\
    " \
    -trimpath \
    -o /go/bin/kubernetes-scanner

FROM gcr.io/distroless/static

COPY --from=build /go/bin/kubernetes-scanner /
ENTRYPOINT ["/kubernetes-scanner"]
