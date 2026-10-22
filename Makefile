VERSION ?= 0.10.1
LDFLAGS := -s -w -X github.com/Cal4Care-Developers/RingQswitch/internal/core.Version=$(VERSION)
# Prefer a Go installed from go.dev in /usr/local/go over an older distro
# package (sudo drops /usr/local/go/bin from PATH).
export PATH := /usr/local/go/bin:$(PATH)

.PHONY: build test test-db race vet interop run install container clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/ringqswitch ./cmd/ringqswitch
	go build -ldflags "$(LDFLAGS)" -o bin/rq_cli ./cmd/rq_cli

test:
	go test ./...

# Database integration tests (PostgreSQL). They work in a throw-away schema:
#   make test-db RINGQ_TEST_DSN=postgres://user:pass@127.0.0.1/ringq
# Add RINGQ_TEST_LIKE_SCHEMA=public to copy your real table definitions.
test-db:
	@test -n "$(RINGQ_TEST_DSN)" || { echo "set RINGQ_TEST_DSN=postgres://user:pass@host/db"; exit 2; }
	RINGQ_TEST_DSN="$(RINGQ_TEST_DSN)" go test -count=1 -v ./internal/db/

race:
	go test -race ./...

vet:
	gofmt -l cmd internal | (! grep .) && go vet ./...

# Interoperability tests with an independent SIP traffic generator
# (apt install sip-tester).
interop:
	bash test/interop/run.sh

run: build
	bin/ringqswitch -conf conf

# Invoked through bash so it works even if the execute bit was lost in transfer.
install: build
	bash packaging/install.sh

# Container image; CONTAINER_TOOL is the program that builds it.
CONTAINER_TOOL ?= docker
container:
	$(CONTAINER_TOOL) build -f Containerfile -t ringqswitch:$(VERSION) .

clean:
	rm -rf bin
