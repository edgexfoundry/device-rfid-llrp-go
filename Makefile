.PHONY: build test clean prepare update docker

GO = CGO_ENABLED=0 GO111MODULE=on go

MICROSERVICES=cmd/device-rfid-llrp-go

.PHONY: $(MICROSERVICES)

DOCKERS=docker_device_rfid_llrp_go
.PHONY: $(DOCKERS)

VERSION=$(shell cat ./VERSION 2>/dev/null || echo 0.0.0)

GIT_SHA=$(shell git rev-parse HEAD)
GOFLAGS=-ldflags "-X github.com/edgexfoundry/device-rfid-llrp-go.Version=$(VERSION)"

build: $(MICROSERVICES)

cmd/device-rfid-llrp-go:
	$(GO) build $(GOFLAGS) -o $@ ./cmd

test:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) vet ./...
	gofmt -l .
	[ "`gofmt -l .`" = "" ]
	./bin/test-go-mod-tidy.sh
	./bin/test-attribution-txt.sh

clean:
	rm -f $(MICROSERVICES)

update:
	$(GO) mod download

docker: $(DOCKERS)

docker_device_rfid_llrp_go:
	docker build \
        --build-arg http_proxy \
        --build-arg https_proxy \
		--label "git_sha=$(GIT_SHA)" \
		-t edgexfoundry/docker-device-rfid-llrp-go:$(GIT_SHA) \
		-t edgexfoundry/docker-device-rfid-llrp-go:$(VERSION)-dev \
		.
