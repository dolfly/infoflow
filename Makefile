# Go build targets for local verification and iCode CI.

HOMEDIR := $(shell pwd)
OUTDIR  := $(HOMEDIR)/output

# 设置编译时所需要的 go 环境
export GOENV = $(HOMEDIR)/go.env

GOPKGS  := $$(go list ./...| grep -vE "vendor")

# make, make all
all: prepare compile package

prepare:
	git version     # 低于 2.17.1 可能不能正常工作
	go env          # 打印出 go 环境信息，可用于排查问题
	go mod download || go mod download -x  # 下载 依赖

#make compile
compile: build
build:
	go build -o $(HOMEDIR)/infoflow-go-sdk

# make test, test your code
test: prepare
	go test -race -timeout=120s -v -cover $(GOPKGS) -coverprofile=coverage.out | tee unittest.txt

# Execute a control-plane resolved regression plan. PLAN and OUT must be
# absolute paths so evidence cannot accidentally land in the source tree.
regression-test:
	@test -n "$(PLAN)" -a -n "$(OUT)" || (echo "usage: make regression-test PLAN=/absolute/plan.json OUT=/absolute/result.json" >&2; exit 2)
	go run ./scripts/regression_test --plan "$(PLAN)" --out "$(OUT)"

# make package
package:
	rm -rf $(OUTDIR)
	mkdir -p $(OUTDIR)
	mv infoflow-go-sdk  $(OUTDIR)/

# make clean
clean:
	go clean
	rm -rf $(OUTDIR)

# avoid filename conflict and speed up build
.PHONY: all prepare compile test regression-test package clean build
