VERSION ?= 0.1.1
GO ?= go

.PHONY: build test dist rpm clean
build:
	mkdir -p bin
	$(GO) build -buildvcs=false -trimpath -ldflags '-s -w -X main.version=$(VERSION)' -o bin/setupvault ./cmd/setupvault

test:
	$(GO) vet ./...
	$(GO) test -race ./...

dist:
	$(GO) mod vendor
	mkdir -p dist
	tar --exclude=./.git --exclude=./bin --exclude=./dist --transform='s,^\.,setupvault-$(VERSION),' -czf dist/setupvault-$(VERSION).tar.gz .

rpm: dist
	mkdir -p dist/rpmbuild
	rpmbuild -ba --define '_topdir $(CURDIR)/dist/rpmbuild' --define '_sourcedir $(CURDIR)/dist' packaging/setupvault.spec

clean:
	rm -rf bin dist vendor
