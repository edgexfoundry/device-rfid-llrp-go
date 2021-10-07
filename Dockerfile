#
# Copyright (c) 2021 Intel Corporation
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#

ARG BASE=golang:1.16-alpine3.14
FROM ${BASE} AS builder

ARG MAKE='make build'
ARG ALPINE_PKG_BASE="make git"
ARG ALPINE_PKG_EXTRA=""

RUN sed -e 's/dl-cdn[.]alpinelinux.org/nl.alpinelinux.org/g' -i~ /etc/apk/repositories
RUN apk add --no-cache ${ALPINE_PKG_BASE} ${ALPINE_PKG_EXTRA}

WORKDIR $GOPATH/src/github.com/edgexfoundry/device-rfid-llrp-go

COPY . .
RUN [ ! -d "vendor" ] && go mod download all || echo "skipping..."

RUN go mod tidy
RUN $MAKE

FROM alpine:3.14

LABEL license='SPDX-License-Identifier: Apache-2.0' \
  copyright='Copyright (c) 2021: Intel'

COPY --from=builder /go/src/github.com/edgexfoundry/device-rfid-llrp-go/LICENSE /
COPY --from=builder /go/src/github.com/edgexfoundry/device-rfid-llrp-go/Attribution.txt /
COPY --from=builder /go/src/github.com/edgexfoundry/device-rfid-llrp-go/cmd /

EXPOSE 49989

ENTRYPOINT ["/device-rfid-llrp-go"]
CMD ["-cp=consul.http://edgex-core-consul:8500", "--confdir=/res", "--registry"]
