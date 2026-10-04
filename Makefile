APP := stepanel
GO ?= go
LDFLAGS := -s -w -X github.com/cyberducttape/StePanel.Commit=$${GIT_COMMIT:-dev} -X github.com/cyberducttape/StePanel.BuildDate=$$(date -u +%Y-%m-%dT%H:%M:%SZ)

.PHONY: all build test test-race fmt fmt-check vet coverage coverage-check fuzz-smoke check recovery-drill mixed-load production-gate audit release-check clean

# The test suite exercises SQLite workers and helper subprocesses. Keep the
# default local targets within a modest process budget so a developer's host
# does not fail before the actual tests run. Override these variables only when
# deliberately testing with more concurrency.
TEST_PARALLELISM ?= 1
TEST_PROCS ?= 2
RACE_PROCS ?= 1
TEST_TIMEOUT ?= 10m

all: check build

build:
	$(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(APP) ./cmd/stepanel

test:
	GOMAXPROCS=$(TEST_PROCS) $(GO) test -p $(TEST_PARALLELISM) -timeout $(TEST_TIMEOUT) ./...

test-race:
	GOMAXPROCS=$(RACE_PROCS) $(GO) test -p $(TEST_PARALLELISM) -race -timeout $(TEST_TIMEOUT) ./...

fmt:
	$(GO) fmt ./...

fmt-check:
	@test -z "$$($(GO)fmt -l .)"

coverage:
	GOMAXPROCS=$(TEST_PROCS) $(GO) test -p $(TEST_PARALLELISM) -timeout $(TEST_TIMEOUT) ./... -coverprofile=coverage.out -covermode=atomic

coverage-check: coverage
	bash scripts/check-coverage.sh coverage.out

fuzz-smoke:
	GOMAXPROCS=1 GOFLAGS=-p=1 $(GO) test -run=^$$ -fuzz=FuzzSafeUser -fuzztime=10s .
	GOMAXPROCS=1 GOFLAGS=-p=1 $(GO) test -run=^$$ -fuzz=FuzzValidBackupName -fuzztime=10s .
	GOMAXPROCS=1 GOFLAGS=-p=1 $(GO) test -run=^$$ -fuzz=FuzzManagedDatabaseIdentifier -fuzztime=10s .

vet:
	$(GO) vet ./...

check: fmt-check vet test

recovery-drill:
	bash deploy/lab/run-recovery-drills.sh "$${RECOVERY_DRILL_OUTPUT:-/tmp/stepanel-recovery-drills.md}"

mixed-load: build
	bash scripts/e2e-mixed-load-test.sh ./$(APP)

production-gate:
	bash scripts/production-gate.sh

audit: fmt-check vet test test-race recovery-drill release-check

release-check:
	./scripts/check-release.sh

clean:
	rm -f $(APP) coverage.out
