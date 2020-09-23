GO = CGO_ENABLED=0 GO111MODULE=on go

MICROSERVICES=cmd/device-llrp-go
DOCKERS=docker_device_llrp_go

.PHONY: $(MICROSERVICES) $(DOCKERS) build test clean update run fmt list-subnets list-subnets-all auto-configure discover

VERSION=$(shell cat ./VERSION 2>/dev/null || echo 0.0.0)
GIT_SHA=$(shell git rev-parse HEAD)

GOFLAGS=-ldflags "-X github.impcloud.net/RSP-Inventory-Suite/device-llrp-go.Version=$(VERSION)"

consul_url = http://localhost:8500

build: $(MICROSERVICES)

cmd/device-llrp-go:
	$(GO) build $(GOFLAGS) -o $@ ./cmd

test:
	go test -coverprofile=coverage.out ./...
	go vet ./...
	./bin/test-attribution.sh
	./bin/test-go-mod-tidy.sh

clean:
	rm -f $(MICROSERVICES)

update:
	$(GO) mod download

docker: $(DOCKERS)

docker_device_llrp_go:
	docker build \
		--build-arg http_proxy \
		--build-arg https_proxy \
		--build-arg HTTP_PROXY \
		--build-arg HTTPS_PROXY \
		--label "git_sha=$(GIT_SHA)" \
		-t edgexfoundry/docker-device-llrp-go:$(GIT_SHA) \
		-t edgexfoundry/docker-device-llrp-go:$(VERSION)-dev \
		.

run: cmd/device-llrp-go
	cd ./cmd && ./device-llrp -cp=consul://localhost:8500 -confdir=res

fmt:
	go fmt ./...

# detects the systems network interfaces and groups them by virtual or non-virtual
# it detects if an interface is virtual or not by seeing whether it is symlinked
# to the /sys/devices/virtual directory.
# usage: $(call get_ifaces,[arguments to grep command])
define get_ifaces =
	for x in /sys/class/net/*; do \
  		printf "$$(basename $$x) $$(realpath $$x)\n"; \
	done | grep $1 "/sys/devices/virtual" \
		 | cut -d ' ' -f 1 \
		 | xargs echo -n
endef

# list of physical network interfaces
physical_ifaces = $(shell $(call get_ifaces,-v))
# list of virtual network interfaces
virtual_ifaces = $(shell $(call get_ifaces,-e))
# macro to print subnet for each passed in interface
# usage: $(call print_subnets,[arguments to grep command])
print_subnets = ip route | grep src | grep $(addprefix -e ,$1) | awk '{printf "%$(2)s | %s\n", $$3, $$1}'
# print physical subnets of this machine as a comma separated list
get_physical_subnets = ip route | grep src | grep $(addprefix -e ,$(physical_ifaces)) | awk '{printf "%s,", $$1}' | sed 's/,$$//g'

# print out subnets for physical and optionally virtual network interfaces
# params: (includeVirtual bool)
define list_subnets =
	@printf "\n\e[1;36mPhysical Interface\e[0m\e[1m | Subnet/CIDR\e[0m\n"
	@$(call print_subnets,$(physical_ifaces),18)

	$(if $1, \
		@printf "\n\e[1;33mVirtual Interface\e[0m\e[1m | Subnet/CIDR\e[0m\n"; \
		$(call print_subnets,$(virtual_ifaces),17) \
		,)

	@printf "\nCopy the subnet information of the line that matches the subnet your LLRP readers are attached to into EdgeX consul here:\n$(consul_url)/ui/dc1/kv/edgex/devices/1.0/edgex-device-llrp/Driver/DiscoverySubnets/edit\n\n"
endef

# list subnets for all connected physical network interfaces
list-subnets:
	$(call list_subnets,)

# list subnets for all connected physical AND virtual network interfaces
list-subnets-all:
	$(call list_subnets,true)

# detect physical subnet information and insert into consul
auto-configure:
	$(eval retcode := \
		$(shell curl \
			--request GET \
			-w "%{http_code}" \
			-o /dev/null \
			-s \
			$(consul_url)/v1/kv/edgex/devices/1.0/edgex-device-llrp/Driver/DiscoverySubnets))

	@if [ $(retcode) -ne 200 ]; then \
  		echo "Error: consul returned an HTTP status code of $(retcode)"; \
  		exit $(retcode); \
  	fi

	curl \
		--request PUT \
		--data "$$($(get_physical_subnets))" \
		$(consul_url)/v1/kv/edgex/devices/1.0/edgex-device-llrp/Driver/DiscoverySubnets

	@echo

# force a discovery by calling the device service http endpoint
discover:
	curl -X POST localhost:51992/api/v1/discovery
	@echo

