PROJECTNAME = $(shell basename "$(PWD)")

include project.properties

PB_RELEASES = https://github.com/protocolbuffers/protobuf/releases

BASE_DIR   = $(shell pwd)

PROTOC_DIR = $(BASE_DIR)/.protoc
PROTO_DIR  = $(BASE_DIR)/proto
STUB_DIR   = $(BASE_DIR)/pkg/pb
TMP_DIR    = $(BASE_DIR)/.tmp

PROTO_FILES = $(shell find $(PROTO_DIR) -type f -name '*.proto')

STUBS_MARKER = $(TMP_DIR)/stubs.marker

.PHONY: all help clean deps

all: help

## help: Prints help
help: Makefile
	@echo "Choose a command in "$(PROJECTNAME)":"
	@sed -n 's/^##//p' $< | column -t -s ':' |  sed -e 's/^/ /'

## clean: Cleans the project
clean:
	@echo "Cleaning the project..."
	@rm -rf $(BIN_DIR) $(TMP_DIR)

## stubs: Generates stubs
stubs: $(STUBS_MARKER)

$(STUBS_MARKER): $(PROTO_FILES)
	@echo "Generating Protobuf stubs..."
	@rm -rf $(STUB_DIR)
	@mkdir -p $(STUB_DIR) $(TMP_DIR)
	@$(PROTOC_DIR)/bin/protoc \
      --proto_path=$(PROTO_DIR)/$(PROTOCOL_VERSION) \
      --go_out=$(STUB_DIR) \
      --go-grpc_out=$(STUB_DIR) \
     $?
	@mkdir -p $(@D)
	@touch $@

## deps: Installs dependencies
deps:
	@echo "Installing dependencies..."
	@rm -rf $(PROTOC_DIR)
	@mkdir -p $(PROTOC_DIR)
	@curl -s -LO $(PB_RELEASES)/download/v$(PB_VERSION)/protoc-$(PB_VERSION)-linux-x86_64.zip
	@unzip -o -qq protoc-$(PB_VERSION)-linux-x86_64.zip -d $(PROTOC_DIR)
	@rm protoc-$(PB_VERSION)-linux-x86_64.zip
	@go install tool
