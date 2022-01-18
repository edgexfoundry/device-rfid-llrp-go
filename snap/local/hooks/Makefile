GO=GO111MODULE=on go
HOOKS=cmd/configure/configure cmd/install/install

build: tidy $(HOOKS)

tidy:
	go mod tidy

cmd/configure/configure:
	$(GO) build -o $@ ./cmd/configure

cmd/install/install:
	$(GO) build -o $@ ./cmd/install

clean:
	rm -f $(HOOKS)

