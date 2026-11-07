FROM --platform=$BUILDPLATFORM golang:1.26.5-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=$GOPROXY
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/cluster-api-provider-kuberoot ./cmd/cluster-api-provider-kuberoot
COPY pkg ./pkg
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /manager ./cmd/cluster-api-provider-kuberoot

FROM scratch
COPY --from=build /manager /manager
USER 65532:65532
ENTRYPOINT ["/manager"]
