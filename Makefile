NAME = k8s-device-scalability-tester

all: gocheck static

# info embedded to the binary
PROJECT = github.com/intel/$(NAME)
COMMIT  = $(shell git rev-parse --short HEAD)
BRANCH  = $(shell git branch --show-current)
VERSION = $(shell git describe --tags --abbrev=0)

GOVERSION = $(shell go version | sed 's/^[^0-9]*//' | cut -d' ' -f1)
BUILDUSER = $(shell git config user.email)
BUILDDATE = $(shell date "+%Y%m%d-%T")

# build static PIE version
BUILDMODE  = -buildmode=pie
EXTLDFLAGS = -static-pie

# build static version
#BUILDMODE =
#EXTLDFLAGS = -static

GOTAGS = osusergo,netgo,static

LDFLAGS = \
-s -w -linkmode external -extldflags $(EXTLDFLAGS) \
-X $(PROJECT)/version.GoVersion=$(GOVERSION) \
-X $(PROJECT)/version.BuildUser=$(BUILDUSER) \
-X $(PROJECT)/version.BuildDate=$(BUILDDATE) \
-X $(PROJECT)/version.Version=$(VERSION) \
-X $(PROJECT)/version.Revision=$(COMMIT) \
-X $(PROJECT)/version.Branch=$(BRANCH)

BACKEND_SRC  = $(wildcard cmd/backend/*.go)
CLIENT_SRC   = $(wildcard cmd/client/*.go)
FRONTEND_SRC = $(wildcard cmd/frontend/*.go)


# static binaries
#
# packages: golang (v1.18 or newer)
static: tester-backend tester-client tester-frontend

tester-backend: $(BACKEND_SRC)
	go build $(BUILDMODE) -tags $(GOTAGS) -ldflags "$(LDFLAGS)" -o $@ $^

tester-frontend: $(FRONTEND_SRC)
	go build $(BUILDMODE) -tags $(GOTAGS) -ldflags "$(LDFLAGS)" -o $@ $^

tester-client: $(CLIENT_SRC)
	go build $(BUILDMODE) -tags $(GOTAGS) -ldflags "$(LDFLAGS)" -o $@ $^


# memory analysis binary versions
#
# packages: clang
msan: tester-backend-msan tester-client-msan tester-frontend-msan

# "-msan" requires "CC=clang", dynamic
tester-backend-msan: $(BACKEND_SRC)
	CC=clang go build -msan $(BUILDMODE) -o $@ $^

tester-client-msan: $(CLIENT_SRC)
	CC=clang go build -msan $(BUILDMODE) -o $@ $^

tester-frontend-msan: $(FRONTEND_SRC)
	CC=clang go build -msan $(BUILDMODE) -o $@ $^


# "-s -w" ldflags would remove debug symbols...
RACE_LDFLAGS = -linkmode external -extldflags -static

# data race detection binaries
race: tester-backend-race tester-client-race tester-frontend-race

# race detector does not work with PIE
tester-backend-race: $(BACKEND_SRC)
	go build -race -ldflags "$(RACE_LDFLAGS)" -tags $(GOTAGS) -o $@ $^

tester-client-race: $(CLIENT_SRC)
	go build -race -ldflags "$(RACE_LDFLAGS)" -tags $(GOTAGS) -o $@ $^

tester-frontend-race: $(FRONTEND_SRC)
	go build -race -ldflags "$(RACE_LDFLAGS)" -tags $(GOTAGS) -o $@ $^


# packages: golang-x-lint (Fedora)
# or: go get -u golang.org/x/lint/golint
gocheck:
	go fmt ./...
	golint ./...
	go vet ./...

golint:
	golangci-lint run  ./...


# checks for auxiliary files / test scripts

# generate report output
# packages: hadolint
hadolint:
	hadolint -v -V 2>&1
	rpm -q hadolint
	@for c in *.docker; do \
		sha256sum $$c; \
		grep -B1 -A1 "hadolint *ignore" $$c; \
		hadolint $$c; \
	done

# packages: yamllint
yamllint:
	yamllint -d relaxed --no-warnings .

# packages: shellcheck
shellcheck:
	find . -name '*.sh' | xargs shellcheck

check: gocheck golint shellcheck yamllint hadolint


mod:
	go mod tidy

zip:
	git archive -o $(NAME).zip --prefix $(NAME)/ HEAD

clean:
	rm -rf tester-frontend* tester-backend* tester-client*

goclean: clean
	go clean --modcache

.PHONY: static msan race gocheck golint hadolint yamllint \
	shellcheck check mod clean goclean
