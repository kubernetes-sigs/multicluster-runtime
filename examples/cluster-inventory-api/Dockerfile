# Copyright The Kubernetes Authors.
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

ARG GO_VERSION=1.26.3
FROM golang:${GO_VERSION} AS builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY providers/cluster-inventory-api/go.mod providers/cluster-inventory-api/go.sum providers/cluster-inventory-api/
RUN cd providers/cluster-inventory-api && go mod download

COPY examples/cluster-inventory-api/go.mod examples/cluster-inventory-api/go.sum examples/cluster-inventory-api/
RUN cd examples/cluster-inventory-api && go mod download

COPY . .
RUN cd examples/cluster-inventory-api && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
      -trimpath -ldflags="-s -w" \
      -o /out/cluster-inventory-api-example .
RUN chmod 0555 /out/cluster-inventory-api-example

FROM scratch
COPY --from=builder /out/cluster-inventory-api-example /cluster-inventory-api-example
USER 65532:65532
ENTRYPOINT ["/cluster-inventory-api-example"]
