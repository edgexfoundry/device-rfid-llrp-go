.PHONY: build test clean prepare update docker

GO = CGO_ENABLED=0 GO111MODULE=on go

MICROSERVICES=cmd/device-llrp

.PHONY: $(MICROSERVICES)

DOCKERS=docker_device_llrp_go

.PHONY: $(DOCKERS)

VERSION=$(shell cat ./VERSION 2>/dev/null || echo 0.0.0)
GIT_SHA=$(shell git rev-parse HEAD)

GOFLAGS=-ldflags "-X github.impcloud.net/RSP-Inventory-Suite/device-llrp-go.Version=$(VERSION)"

build: $(MICROSERVICES)
	$(GO) build ./...

cmd/device-llrp:
	$(GO) build $(GOFLAGS) -o $@ ./cmd

test:
	$(GO) test ./... -coverprofile=coverage.out

clean:
	rm -f $(MICROSERVICES)

docker: $(DOCKERS)

docker_device_llrp_go:
	docker build \
 	--build-arg http_proxy \
    --build-arg https_proxy \
		--label "git_sha=$(GIT_SHA)" \
		-t edgexfoundry/docker-device-llrp-go:$(GIT_SHA) \
		-t edgexfoundry/docker-device-llrp-go:$(VERSION)-dev \
		.

run:
	docker-compose -f docker-compose-geneva-redis-no-secty.yml up -d

stop:
	docker-compose -f docker-compose-geneva-redis-no-secty.yml down
