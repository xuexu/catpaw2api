.PHONY: all linux windows test docker clean

GO ?= go
BINDIR := bin
# 禁用 VCS stamping：本仓库非标准 git 布局，go build 会报 error obtaining VCS status。
BUILDFLAGS := -buildvcs=false

all: linux

linux:
	@mkdir -p $(BINDIR)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build $(BUILDFLAGS) -o $(BINDIR)/catpaw2api ./cmd/server
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build $(BUILDFLAGS) -o $(BINDIR)/catpaw2api-login ./cmd/login
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build $(BUILDFLAGS) -o $(BINDIR)/catpaw2api-credit ./cmd/credit
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build $(BUILDFLAGS) -o $(BINDIR)/catpaw2api-apply ./cmd/apply
	@echo "linux binaries -> $(BINDIR)/"

windows:
	@mkdir -p $(BINDIR)
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) build $(BUILDFLAGS) -o $(BINDIR)/catpaw2api.exe ./cmd/server
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) build $(BUILDFLAGS) -o $(BINDIR)/catpaw2api-login.exe ./cmd/login
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) build $(BUILDFLAGS) -o $(BINDIR)/catpaw2api-credit.exe ./cmd/credit
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) build $(BUILDFLAGS) -o $(BINDIR)/catpaw2api-apply.exe ./cmd/apply
	@echo "windows binaries -> $(BINDIR)/"

test:
	$(GO) test $(BUILDFLAGS) ./...

docker:
	docker compose up -d --build

clean:
	rm -rf $(BINDIR) data
