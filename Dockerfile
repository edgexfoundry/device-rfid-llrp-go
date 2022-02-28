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

ARG BASE=golang:1.17-alpine3.15
FROM ${BASE} AS builder

ARG MAKE='make build'
ARG ALPINE_PKG_BASE="make git gcc libc-dev zeromq-dev"
ARG ALPINE_PKG_EXTRA=""

RUN sed -e 's/dl-cdn[.]alpinelinux.org/nl.alpinelinux.org/g' -i~ /etc/apk/repositories
RUN apk add --no-cache ${ALPINE_PKG_BASE} ${ALPINE_PKG_EXTRA}

WORKDIR /app

COPY go.mod vendor* ./
RUN [ ! -d "vendor" ] && go mod download all || echo "skipping..."

COPY . .
RUN $MAKE

FROM alpine:3.14

LABEL license='SPDX-License-Identifier: Apache-2.0' \
  copyright='Copyright (c) 2021: Intel'

RUN sed -e 's/dl-cdn[.]alpinelinux.org/nl.alpinelinux.org/g' -i~ /etc/apk/repositories
RUN apk add --update --no-cache zeromq dumb-init

COPY --from=builder /app/LICENSE /
COPY --from=builder /app/Attribution.txt /
COPY --from=builder /app/cmd/ /

EXPOSE 59989

ENTRYPOINT ["/device-rfid-llrp"]
CMD ["-cp=consul.http://edgex-core-consul:8500", "--confdir=/res", "--registry"]
