export PATH := $(HOME)/go/bin:$(PATH)

BIN_DIR := bin

.PHONY: all build test clean start stop run-client csg-status proto tmux sandbox-build sandbox-up sandbox-down sandbox-reset sandbox-status sandbox-mutate

all: build

# 重新生成 Protobuf 代码与二进制描述集（保留源码注释与位置元数据供全生命周期文档解析）
proto:
	@echo "Compiling Protobuf definitions and descriptor set..."
	cd .. && protoc --go_out=. --go_opt=module=code-contract \
		--descriptor_set_out=demo/demo.pb --include_imports --include_source_info \
		demo/proto/common.proto \
		demo/proto/order.proto \
		demo/proto/account.proto \
		demo/proto/inventory.proto \
		demo/proto/payment.proto

# 编译微服务组件、CSG 控制面与 Agent 二进制产物
build: proto
	@echo "Building all CROS RPC binaries, csgserver and Agent into $(BIN_DIR)..."
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/csgserver ./cmd/csgserver
	go build -o $(BIN_DIR)/backend-account ./cmd/backend-account
	go build -o $(BIN_DIR)/backend-inventory ./cmd/backend-inventory
	go build -o $(BIN_DIR)/backend-payment ./cmd/backend-payment
	go build -o $(BIN_DIR)/sut-order ./cmd/sut-order
	go build -o $(BIN_DIR)/client ./cmd/client
	$(MAKE) -C .. agent
	cp --remove-destination ../code-contract-agent $(BIN_DIR)/code-contract-agent
	go build -o $(BIN_DIR)/proto-sandbox ./cmd/proto-sandbox
	@echo "Build complete."

# 编译 Proto & 契约治理全链路沙箱 CLI
sandbox-build:
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/proto-sandbox ./cmd/proto-sandbox

# 一键初始化本地多仓 Proto 沙箱（含同步、全池协调与 Group A 描述集激活）
sandbox-up: sandbox-build
	@./$(BIN_DIR)/proto-sandbox up

# 一键彻底清理本地沙箱数据与本地沙箱缓存
sandbox-down: sandbox-build
	@./$(BIN_DIR)/proto-sandbox down

# 一键重置沙箱
sandbox-reset: sandbox-build
	@./$(BIN_DIR)/proto-sandbox reset

# 查看沙箱状态
sandbox-status: sandbox-build
	@./$(BIN_DIR)/proto-sandbox status

# 触发动态演进剧本 (示例: make sandbox-mutate SCENARIO=breaking)
sandbox-mutate: sandbox-build
	@./$(BIN_DIR)/proto-sandbox mutate $(SCENARIO)

# 运行单元测试与集成测试
test:
	@echo "Running all demo unit and integration tests..."
	go test -v ./...

# 一键拉起后台服务集群 (纯后台静默运行)
start: build
	@./scripts/start_all.sh

# 一键启动 Tmux 分屏可视化集群
tmux:
	@./start.sh

# 一键停止服务集群
stop:
	@./scripts/stop_all.sh

# 启动 TUI 交互式客户端（默认经由 CSG 控制面动态寻址 SUT）
run-client:
	@./bin/client --csg="127.0.0.1:31806" --addr="127.0.0.1:9001"

# 查询当前 CSG 注册服务实例与客户端订阅目录
csg-status:
	@./bin/csgserver --query --addr="127.0.0.1:31806" --monitor="127.0.0.1:31901" --op=all

# 清理构建产物
clean:
	@echo "Cleaning up $(BIN_DIR)..."
	@rm -rf $(BIN_DIR)
