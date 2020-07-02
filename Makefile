.PHONY: build test clean prepare update docker

GO = CGO_ENABLED=0 GO111MODULE=on go

MICROSERVICES=cmd/device-llrp

.PHONY: $(MICROSERVICES)

DOCKERS=docker_device_llrp_go
COMPOSE_FILE=docker-compose-geneva-redis-no-secty.yml
DOCKER_COMPOSE=docker-compose -f $(COMPOSE_FILE)

.PHONY: $(DOCKERS) iterate test clean down rm-volumes stop tail run up deploy fmt

VERSION=$(shell cat ./VERSION 2>/dev/null || echo 0.0.0)
GIT_SHA=$(shell git rev-parse HEAD)

GOFLAGS=-ldflags "-X github.impcloud.net/RSP-Inventory-Suite/device-llrp-go.Version=$(VERSION)"

build: $(MICROSERVICES)

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

run up:
	$(DOCKER_COMPOSE) up

deploy:
	$(DOCKER_COMPOSE) up -d

iterate: fmt docker deploy tail

stop:
	$(DOCKER_COMPOSE) stop

down:
	$(DOCKER_COMPOSE) down

tail:
	docker logs -f $(shell docker ps -qf name=llrp)

rm-volumes:
	for x in $$(docker volume ls -q | grep 'device-llrp'); do \
  		docker volume rm $$x; \
	done

fmt:
	go fmt ./...
