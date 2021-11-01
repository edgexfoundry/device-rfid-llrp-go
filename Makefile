.PHONY: build test clean prepare update docker

GO=CGO_ENABLED=0 GO111MODULE=on go
GOCGO=CGO_ENABLED=1 GO111MODULE=on go

MICROSERVICES=cmd/device-rfid-llrp-go

.PHONY: $(MICROSERVICES)

DOCKERS=docker_device_rfid_llrp_go
.PHONY: $(DOCKERS)

VERSION=$(shell cat ./VERSION 2>/dev/null || echo 0.0.0)

GIT_SHA=$(shell git rev-parse HEAD)
GOFLAGS=-ldflags "-X github.com/edgexfoundry/device-rfid-llrp-go.Version=$(VERSION)"

tidy:
	go mod tidy

build: $(MICROSERVICES)

cmd/device-rfid-llrp-go:
	$(GOCGO) build $(GOFLAGS) -o $@ ./cmd

test:
	$(GOCGO) test -coverprofile=coverage.out ./...
	$(GOCGO) vet ./...
	gofmt -l $$(find . -type f -name '*.go'| grep -v "/vendor/")
	[ "`gofmt -l $$(find . -type f -name '*.go'| grep -v "/vendor/")`" = "" ]
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

vendor:
	$(GO) mod vendor
