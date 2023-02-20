FROM golang:1.20 as build

WORKDIR /go/src/app
COPY . .

ARG COMMIT_SHA
ARG GIT_TAG

RUN go mod download
RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w \
        -X github.com/snyk/kubernetes-scanner/build.commitSHA=$COMMIT_SHA \
        -X github.com/snyk/kubernetes-scanner/build.tag=$GIT_TAG\
    " \
    -trimpath \
    -o /go/bin/kubernetes-scanner

FROM gcr.io/distroless/static

COPY --from=build /go/bin/kubernetes-scanner /
CMD ["/kubernetes-scanner"]
