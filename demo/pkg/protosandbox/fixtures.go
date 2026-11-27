package protosandbox

import (
	"fmt"
	"strings"
)

const (
	// SandboxURLPrefix 标识所有沙箱模拟仓库的唯一虚拟 URL 前缀，用于精准识别与清理。
	SandboxURLPrefix = "https://git.sandbox.local/sandbox-demo/"
	// SandboxCacheSubdir 为宿主机共享 codes/ 缓存中的沙箱隔离根目录。
	SandboxCacheSubdir = "sandbox-demo"
	// SandboxSharedBaseRel 为 R4 共享基座目录（位于 <codesDir>/sandbox-demo/shared-base/include）。
	SandboxSharedBaseRel = "sandbox-demo/shared-base/include"
	// SandboxVocabDomain 标识沙箱播种的标准词汇领域，用于安全清理。
	SandboxVocabDomain = "SANDBOX_DEMO"
	// SandboxTagV1 为初始基线提交的 Git 标签，供 mutate rollback 回滚恢复。
	SandboxTagV1 = "sandbox-v1"
)

// RepoFixture 描述一个沙箱模拟代码仓的元数据与初始文件集。
type RepoFixture struct {
	Name           string
	Group          string
	ServiceGroup   string
	Branch         string
	SkipLocalCache bool
	Files          map[string][]byte
	StripPrefixes  []string
	Description    string
	IsGroupA       bool
}

// RepoURL 返回符合 ResolveRepoLocalPath 映射规则的沙箱仓库地址。
func (f RepoFixture) RepoURL() string {
	return fmt.Sprintf("%s%s/%s.git", SandboxURLPrefix, strings.Trim(f.Group, "/"), strings.Trim(f.Name, "/"))
}

// SharedBaseFiles 返回 R4 共享基座收割器使用的真实基座 .proto 文件（位于非 pg 白名单目录 include/ 下）。
func SharedBaseFiles() map[string][]byte {
	return map[string][]byte{
		"ltcommon/pg/OptionsExtensions.proto": []byte(protoOptionsExtensions),
		"sw/pg/BasicTypeMsg.proto":            []byte(protoBasicTypeMsg),
	}
}

// StandardFixtures 返回完整的 A 组（电商全链路联动）+ B 组（现网病灶复刻）14 仓沙箱矩阵。
func StandardFixtures() []RepoFixture {
	return []RepoFixture{
		// ── Group A: 与现有电商 CROS RPC Demo 100% 协议对齐的 Polyrepo ──
		{
			Name:         "sandbox-common-base",
			Group:        "ecom",
			ServiceGroup: "SBX-PLATFORM",
			Branch:       "main",
			IsGroupA:     true,
			Description:  "电商核心领域公共基础库 (BASE_LIB: demo.common) 与多实现支付契约接口库 (demo.payment)",
			Files: map[string][]byte{
				"pg/demo/proto/common.proto":  []byte(protoEcomCommon),
				"pg/demo/proto/payment.proto": []byte(protoEcomPayment),
			},
		},
		{
			Name:         "sandbox-order-gateway",
			Group:        "ecom",
			ServiceGroup: "SBX-TRADE",
			Branch:       "main",
			IsGroupA:     true,
			Description:  "订单交易中枢聚合网关服务仓 (GATEWAY: demo.order.OrderService)",
			Files: map[string][]byte{
				"pg/demo/proto/order.proto": []byte(protoEcomOrder),
			},
		},
		{
			Name:         "sandbox-account-svc",
			Group:        "ecom",
			ServiceGroup: "SBX-ASSET",
			Branch:       "main",
			IsGroupA:     true,
			Description:  "账户资产领域服务仓 (demo.account.AccountService，含跨仓 AST 重复结构体样本)",
			Files: map[string][]byte{
				"pg/demo/proto/account.proto": []byte(protoEcomAccount),
			},
		},
		{
			Name:          "sandbox-inventory-svc",
			Group:         "ecom",
			ServiceGroup:  "SBX-FULFILL",
			Branch:        "main",
			IsGroupA:      true,
			StripPrefixes: []string{"server/pg/proto/"},
			Description:   "库存履约服务仓 (演示 */pg/**/*.proto 深层目录与 ADR-27 strip_prefixes 归一化)",
			Files: map[string][]byte{
				"server/pg/proto/demo/proto/inventory.proto": []byte(protoEcomInventory),
			},
		},
		{
			Name:         "sandbox-payment-alipay",
			Group:        "ecom",
			ServiceGroup: "SBX-PAYMENT",
			Branch:       "main",
			IsGroupA:     true,
			Description:  "支付宝支付通道实现仓 (MULTI_IMPL 实现方 + 通道专属管理服务)",
			Files: map[string][]byte{
				"pg/demo/payment/alipay/alipay_channel.proto": []byte(protoEcomAlipayChannel),
			},
		},
		{
			Name:         "sandbox-payment-wechat",
			Group:        "ecom",
			ServiceGroup: "SBX-PAYMENT",
			Branch:       "main",
			IsGroupA:     true,
			Description:  "微信支付通道实现仓 (MULTI_IMPL 实现方 + 通道专属管理服务)",
			Files: map[string][]byte{
				"pg/demo/payment/wechat/wechat_channel.proto": []byte(protoEcomWechatChannel),
			},
		},

		// ── Group B: 脱敏现网病灶矩阵（覆盖 ADR-26 / ADR-27 / ADR-28 全部核心断点）──
		{
			Name:         "sandbox-fab-motion",
			Group:        "fab",
			ServiceGroup: "SBX-FAB",
			Branch:       "main",
			Description:  "设备运动控制 uService 脏语法仓（BOM/CRLF/C宏/缺syntax/重复package/块内裸import/隐式VoidType/TC非闭包）",
			Files: map[string][]byte{
				"motion/pg/StageMotionImpl.proto": []byte(protoFabMotionImpl),
				"motion/pg/StageMotionMsg.proto":  []byte(protoFabMotionMsg),
				"motion/pg/StageMotionTC.proto":   []byte(protoFabMotionTC),
			},
		},
		{
			Name:         "sandbox-fab-optics",
			Group:        "fab",
			ServiceGroup: "SBX-FAB",
			Branch:       "main",
			Description:  "光学对准服务仓（验证去 /pg 边界保护 extpg/pg.proto、R0 历史变体 alpha/m1 排除与 R3 冲突主副本）",
			Files: map[string][]byte{
				"pg/optics/OpticsAlignService.proto":   []byte(protoFabOpticsService),
				"pg/extpg/OpticsCoeff.proto":           []byte(protoFabOpticsCoeff),
				"pg/pg.proto":                          []byte(protoFabPGFile),
				"pg/alpha/OpticsAlignService.proto":    []byte(protoFabOpticsAlpha),
				"pg/m1/OpticsLegacy.proto":             []byte(protoFabOpticsM1),
				"pg/fab/shared/SharedDiagMsg.proto":    []byte(protoFabSharedDiagPrimary),
				"pg/fab/shared/IdenticalUtilMsg.proto": []byte(protoFabIdenticalUtil),
			},
		},
		{
			Name:         "sandbox-fab-sensor-copy",
			Group:        "fab",
			ServiceGroup: "SBX-FAB",
			Branch:       "main",
			Description:  "传感采集服务仓（含等价与不等价跨仓副本、缺同目录兄弟 import 与错路径 import）",
			Files: map[string][]byte{
				"pg/fab/shared/SharedDiagMsg.proto":    []byte(protoFabSharedDiagDivergent),
				"pg/fab/shared/IdenticalUtilMsg.proto": []byte(protoFabIdenticalUtil),
				"pg/sensor/SensorReadService.proto":    []byte(protoFabSensorService),
				"pg/sensor/SensorCalibMsg.proto":       []byte(protoFabSensorCalibMsg),
			},
		},
		{
			Name:         "sandbox-fab-broken",
			Group:        "fab",
			ServiceGroup: "SBX-FAB",
			Branch:       "main",
			Description:  "不可恢复坏语法仓（验证 Stage B QUARANTINED 隔离区与基线错误分布统计）",
			Files: map[string][]byte{
				"pg/broken/CorruptSyntax.proto": []byte(protoFabCorruptSyntax),
			},
		},
		{
			Name:         "sandbox-3m-legacy",
			Group:        "legacy3m",
			ServiceGroup: "SBX-3M",
			Branch:       "main",
			Description:  "历史集成子系统 SBX-3M 副本仓（验证 R0 exclude_subsystems 排除机制）",
			Files: map[string][]byte{
				"pg/demo/proto/order.proto": []byte(protoEcomOrder),
			},
		},
		{
			Name:         "sandbox-no-proto-app",
			Group:        "tools",
			ServiceGroup: "SBX-TOOLS",
			Branch:       "main",
			Description:  "纯业务代码仓（仅含大写 PG/ 目录，验证严格小写 pg/ 规则、NO_PROTO 静默态及 mutate promote-no-proto）",
			Files: map[string][]byte{
				"README.md":             []byte("# Sandbox Tools App\nThis repository intentionally has no lowercase `pg/` directory.\n"),
				"src/main.go":           []byte("package main\n\nfunc main() {}\n"),
				"PG/IgnoredUpper.proto": []byte("syntax = \"proto3\";\npackage tools.ignored;\nmessage IgnoredMsg { string id = 1; }\n"),
			},
		},
		{
			Name:         "sandbox-unassigned-svc",
			Group:        "misc",
			ServiceGroup: "",
			Branch:       "main",
			Description:  "未分配子系统仓（maps to _unassigned，用于演示 ADR-27 方案 A 导出目标目录重定位）",
			Files: map[string][]byte{
				"pg/legacy/health_check.proto": []byte(protoMiscHealthCheck),
			},
		},
		{
			Name:           "sandbox-missing-cache",
			Group:          "tools",
			ServiceGroup:   "SBX-TOOLS",
			Branch:         "main",
			SkipLocalCache: true,
			Description:    "无本地缓存模拟仓（验证 CACHE_ONLY 模式下 NO_LOCAL_CACHE 静默归类）",
			Files: map[string][]byte{
				"pg/missing/noop.proto": []byte("syntax = \"proto3\";\npackage tools.missing;\nmessage NoopReq { string id = 1; }\n"),
			},
		},
	}
}

// BuildScaleFixtures 按现网比例生成额外 N 个压测仓（约 15% HAS_PROTO、75% NO_PROTO、10% NO_LOCAL_CACHE）。
func BuildScaleFixtures(extraCount int) []RepoFixture {
	if extraCount <= 0 {
		return nil
	}
	subsystems := []string{"SBX-TRADE", "SBX-ASSET", "SBX-FULFILL", "SBX-FAB", "SBX-TOOLS"}
	out := make([]RepoFixture, 0, extraCount)
	for i := 1; i <= extraCount; i++ {
		sg := subsystems[(i-1)%len(subsystems)]
		name := fmt.Sprintf("sandbox-scale-%04d", i)
		mod := i % 20
		switch {
		case mod < 2: // 10% NO_LOCAL_CACHE
			out = append(out, RepoFixture{
				Name:           name,
				Group:          "scale",
				ServiceGroup:   sg,
				Branch:         "main",
				SkipLocalCache: true,
				Description:    "规模压测仓 (NO_LOCAL_CACHE)",
			})
		case mod < 5: // 15% HAS_PROTO
			msgName := fmt.Sprintf("ScaleItem%04d", i)
			svcName := fmt.Sprintf("ScaleService%04d", i)
			pkgName := fmt.Sprintf("scale.svc%04d", i)
			protoSrc := fmt.Sprintf(`syntax = "proto3";

package %s;

service %s {
  rpc Ping(%sReq) returns (%sRsp);
}

message %sReq {
  string trace_id = 1;
}

message %sRsp {
  int32 code = 1;
}
`, pkgName, svcName, msgName, msgName, msgName, msgName)
			out = append(out, RepoFixture{
				Name:         name,
				Group:        "scale",
				ServiceGroup: sg,
				Branch:       "main",
				Description:  "规模压测仓 (HAS_PROTO)",
				Files: map[string][]byte{
					fmt.Sprintf("pg/scale/svc_%04d.proto", i): []byte(protoSrc),
				},
			})
		default: // 75% NO_PROTO
			out = append(out, RepoFixture{
				Name:         name,
				Group:        "scale",
				ServiceGroup: sg,
				Branch:       "main",
				Description:  "规模压测仓 (NO_PROTO)",
				Files: map[string][]byte{
					"README.md": []byte(fmt.Sprintf("# %s\nNo pg proto files.\n", name)),
				},
			})
		}
	}
	return out
}

const protoOptionsExtensions = `syntax = "proto2";

package ltcommon.options;

import "google/protobuf/descriptor.proto";

extend google.protobuf.FieldOptions {
  optional string cmRange = 50001;
  optional bool readOnly = 50002;
  optional string visibility = 50003;
  optional string description = 50004;
  optional string units = 50005;
}
`

const protoBasicTypeMsg = `syntax = "proto2";

// VoidType 平台通用空占位消息（无 package，供 uService 隐式引用时跨包直接解析）
message VoidType {
}

// BasicStatus 通用基础返回状态头
message BasicStatus {
  optional int32 err_code = 1;
  optional string err_msg = 2;
}
`

const protoEcomCommon = `syntax = "proto3";

// 电商核心领域公共基础类型与枚举字典契约 (BASE_LIB: demo.common)
// @owner: platform-arch-team
package demo.common;
option go_package = "code-contract/demo/pkg/proto/commonpb";

// 订单交易终态枚举字典
// @owner: platform-arch-team
enum OrderStatusEnum {
  // 未指定状态（proto3 零值占位，业务响应禁止返回此值）
  ORDER_STATUS_ENUM_UNSPECIFIED = 0;
  // 下单成功：库存已确认扣减、资金扣款与渠道授权全部完成
  ORDER_STATUS_ENUM_SUCCESS = 1;
  // 库存不足失败：预占锁库阶段即拒绝下单
  ORDER_STATUS_ENUM_FAILED_STOCK = 2;
  // 余额不足失败：账户扣款失败，已自动触发库存释放补偿
  ORDER_STATUS_ENUM_FAILED_BALANCE = 3;
  // 支付渠道拒绝失败：第三方支付鉴权未通过，已执行账户退款与释库双补偿
  ORDER_STATUS_ENUM_FAILED_PAYMENT = 4;
  // 订单已取消：用户主动取消且已完成逆向退费与库存归还
  ORDER_STATUS_ENUM_CANCELLED = 5;
}

// 支付渠道路由枚举字典
// @owner: payment-team
enum PaymentChannelEnum {
  // 未指定支付渠道
  PAYMENT_CHANNEL_ENUM_UNSPECIFIED = 0;
  // 支付宝直连收银台通道
  PAYMENT_CHANNEL_ENUM_ALIPAY = 1;
  // 微信支付直连收银台通道
  PAYMENT_CHANNEL_ENUM_WECHAT = 2;
  // 银联老代扣通道
  // @deprecated: 银联老直连网关通道已于 V2 下线，请统一切换至 ALIPAY 或 WECHAT 聚合路由
  PAYMENT_CHANNEL_ENUM_UNIONPAY = 3 [deprecated = true];
}

// 全链路审计与追踪公共扩展头
// @owner: platform-arch-team
message TraceAuditMeta {
  // 全链路追踪 TraceID（与 CROS RPC 报文头 CorrelationID 保持对齐）
  string trace_id = 1;
  // 发起方应用模块编码（如 ORDER_CORE / CLIENT_TUI）
  string caller_module = 2;
  // 请求发起时间戳（Unix 秒级）
  int64 timestamp_unix = 3;
  // 部署可用区标识（如 cn-east-1a）
  string region_code = 4;
}
`

const protoEcomPayment = `syntax = "proto3";

// 统一支付通道公共接口契约 (BASE_LIB 多实现契约: demo.payment.PaymentService)
// @owner: payment-team
package demo.payment;
option go_package = "code-contract/demo/pkg/proto/paymentpb";

import "demo/proto/common.proto";

// 统一支付通道接口契约 (LIB 多实现契约名: demo.payment.PaymentService)
// 各业务实例可发布自定义运行期服务名（如 demo.payment.ALIPAY_PaymentService / demo.payment.WECHAT_PaymentService），
// 或通过共享同名模式运行，并共用同一份契约解码定义。
// @owner: payment-team
service PaymentService {
  // 渠道支付授权扣款：按商户订单金额向指定第三方支付路由发起同步鉴权扣款
  // @owner: payment-team
  // @error_code 4101 ERR_PAY_INVALID_PARAM 支付参数非法或金额越界（<=0）
  // @error_code 4102 ERR_PAY_INSUFFICIENT_QUOTA 商户渠道单笔风控限额超限
  // @error_code 4103 ERR_PAY_CHANNEL_REJECTED 第三方支付渠道网关拒绝授权（如余额不足/风控拦截）
  rpc AuthorizePay(AuthorizePayReq) returns (AuthorizePayRsp);

  // 渠道原路退款补偿：根据原渠道支付流水号执行全额或部分原路退回
  // @owner: payment-team
  // @error_code 4104 ERR_PAY_TX_NOT_FOUND 原支付流水记录不存在
  // @error_code 4105 ERR_PAY_ALREADY_REFUNDED 该笔支付流水已完成全额退款，禁止重复补偿
  rpc RefundPay(RefundPayReq) returns (RefundPayRsp);
}

// AuthorizePayReq 渠道支付授权请求
message AuthorizePayReq {
  // 平台全局订单号
  string order_id = 1;
  // 买家用户唯一标识
  string user_id = 2;
  // 待支付金额（单位：分，必须大于 0）
  int64 amount_cents = 3;
  // 支付渠道文本编码（兼容旧版客户端：ALIPAY / WECHAT）
  string channel = 4;
  // 强类型支付渠道枚举（推荐新接入方使用，替代自由文本 channel）
  demo.common.PaymentChannelEnum channel_enum = 5;
}

// AuthorizePayRsp 渠道支付授权响应
message AuthorizePayRsp {
  // 业务状态码：0=授权成功，4101=参数非法，4102=限额超限，4103=渠道拒绝
  int32 code = 1;
  // 响应描述信息
  string message = 2;
  // 平台全局订单号
  string order_id = 3;
  // 实际承接本次交易的支付渠道编码
  string channel = 4;
  // 第三方渠道支付流水号（如 ALIPAY-TX-... / WECHAT-TX-...）
  string channel_tx_no = 5;
  // 渠道扣款完成时间戳（Unix 秒级）
  int64 paid_at_unix = 6;
}

// RefundPayReq 渠道原路退款补偿请求
message RefundPayReq {
  // 原平台全局订单号
  string order_id = 1;
  // 原第三方渠道支付流水号
  string channel_tx_no = 2;
  // 本次退回金额（单位：分）
  int64 amount_cents = 3;
  // 退款原因说明（如 SAGA_ROLLBACK / USER_CANCEL）
  string reason = 4;
}

// RefundPayRsp 渠道原路退款补偿响应
message RefundPayRsp {
  // 业务状态码：0=退款受理成功，4104=原支付流水不存在，4105=重复退款
  int32 code = 1;
  // 响应描述信息
  string message = 2;
  // 原平台全局订单号
  string order_id = 3;
  // 执行退款的支付渠道编码
  string channel = 4;
  // 渠道退款流水号
  string refund_tx_no = 5;
}
`

const protoEcomOrder = `syntax = "proto3";

// 订单交易中枢聚合网关服务契约 (GATEWAY: demo.order.OrderService)
// 依赖基础契约库 demo.common 与多实现支付契约 demo.payment
// @owner: trade-order-team
package demo.order;
option go_package = "code-contract/demo/pkg/proto/orderpb";

import "demo/proto/common.proto";
import "demo/proto/payment.proto";

// 订单综合服务契约 (Service: demo.order.OrderService)
// 承担电商下单全链路 Saga/TCC 编排职责：并发聚合账户资产与库存行情、协调锁库扣款与动态支付渠道路由
// @owner: trade-order-team
service OrderService {
  // 综合聚合查询：并发查询用户资金账户余额与商品实时可售库存行情
  // @owner: trade-order-team
  // @error_code 1001 ERR_ORDER_INVALID_ARG 用户或 SKU 标识不能为空
  // @error_code 1002 ERR_ORDER_QUOTE_DOWNSTREAM 下游账户或库存服务查询失败
  rpc GetCompositeQuote(CompositeQuoteReq) returns (CompositeQuoteRsp);

  // 提交订单：触发分布式 Saga 编排（预占库存 -> 扣减余额 -> 支付渠道授权 -> 确认库存）
  // @owner: trade-order-team
  // @error_code 1003 ERR_ORDER_STOCK_LOCK_FAILED 库存预占锁定失败（可用库存不足）
  // @error_code 1004 ERR_ORDER_BALANCE_DEBIT_FAILED 账户余额扣减失败（自动触发释放库存补偿）
  // @error_code 1005 ERR_ORDER_PAYMENT_REJECTED 支付渠道拒绝授权（自动触发退还余额 + 释放库存双补偿）
  rpc CreateOrder(CreateOrderReq) returns (CreateOrderRsp);

  // 查询订单详情及履约支付凭证明细
  // @owner: trade-order-team
  // @error_code 1006 ERR_ORDER_NOT_FOUND 目标订单号不存在
  rpc GetOrderDetail(OrderDetailReq) returns (OrderDetailRsp);

  // 取消订单：对已成交订单执行全链路逆向补偿（支付原路退款 -> 退还账户余额 -> 释放还原库存）
  // @owner: trade-order-team
  // @error_code 1006 ERR_ORDER_NOT_FOUND 目标订单号不存在
  // @error_code 1007 ERR_ORDER_CANCEL_NOT_ALLOWED 当前订单状态不允许取消（仅 SUCCESS 状态可取消）
  rpc CancelOrder(CancelOrderReq) returns (CancelOrderRsp);
}

// CompositeQuoteReq 下单前复合行情询价请求
message CompositeQuoteReq {
  // 下单用户唯一标识（如 U1001）
  string user_id = 1;
  // 目标商品 SKU 编码（如 SKU-PHONE-01）
  string sku_id = 2;
}

// CompositeQuoteRsp 下单前复合行情询价响应
message CompositeQuoteRsp {
  // 业务状态码：0=成功，其它=下游校验或参数错误
  int32 code = 1;
  // 响应描述信息
  string message = 2;
  // 用户唯一标识
  string user_id = 3;
  // 用户当前可用余额（单位：分）
  int64 balance_cents = 4;
  // 商品标准 SKU 编码
  string sku_id = 5;
  // 商品名称
  string sku_name = 6;
  // 商品实时单价（单位：分）
  int64 unit_price_cents = 7;
  // 仓库实时剩余可售库存
  int32 available_stock = 8;
}

// CreateOrderReq 创建交易订单请求
message CreateOrderReq {
  // 下单用户唯一标识
  string user_id = 1;
  // 目标商品 SKU 编码
  string sku_id = 2;
  // 购买数量（需大于 0）
  int32 quantity = 3;
  // 客户端幂等防重流水号
  string client_order_ref = 4;
  // 指定外部支付路由渠道：ALIPAY（默认） / WECHAT
  string payment_channel = 5;
}

// CreateOrderRsp 创建交易订单响应
message CreateOrderRsp {
  // 业务状态码：0=交易成功，非 0=下单拒绝并已完成对应回滚补偿
  int32 code = 1;
  // 响应描述信息
  string message = 2;
  // 订单中心生成的全局唯一订单号
  string order_id = 3;
  // 订单成交总金额（单位：分）
  int64 total_amount_cents = 4;
  // 订单终态字符串（SUCCESS / FAILED_STOCK / FAILED_BALANCE / FAILED_PAYMENT）
  string status = 5;
  // 订单创建时间戳（Unix 秒级）
  int64 created_at_unix = 6;
  // 实际路由承接本次交易的支付渠道
  string payment_channel = 7;
  // 支付渠道返回的外部交易流水号
  string payment_tx_no = 8;
  // 强类型订单终态枚举（引用公共契约库 demo.common.OrderStatusEnum）
  demo.common.OrderStatusEnum status_enum = 9;
  // 全链路追踪与审计扩展头
  demo.common.TraceAuditMeta audit_meta = 10;
}

// OrderDetailReq 查询订单详情请求
message OrderDetailReq {
  // 目标订单号
  string order_id = 1;
}

// OrderDetailRsp 查询订单详情响应
message OrderDetailRsp {
  // 业务状态码：0=成功，1006=订单不存在
  int32 code = 1;
  // 响应描述信息
  string message = 2;
  // 全局唯一订单号
  string order_id = 3;
  // 买家用户唯一标识
  string user_id = 4;
  // 商品标准 SKU 编码
  string sku_id = 5;
  // 购买件数
  int32 quantity = 6;
  // 订单总金额（单位：分）
  int64 total_amount_cents = 7;
  // 订单状态字符串
  string status = 8;
  // 订单创建时间戳（Unix 秒级）
  int64 created_at_unix = 9;
  // 支付渠道编码
  string payment_channel = 10;
  // 外部支付流水号
  string payment_tx_no = 11;
  // 强类型订单状态枚举
  demo.common.OrderStatusEnum status_enum = 12;
  // 关联支付渠道最近一次授权回执详情（跨包复用 demo.payment.AuthorizePayRsp）
  demo.payment.AuthorizePayRsp last_payment_receipt = 13;
}

// CancelOrderReq 取消订单请求
message CancelOrderReq {
  // 待取消订单号
  string order_id = 1;
  // 取消原因描述
  string reason = 2;
}

// CancelOrderRsp 取消订单响应
message CancelOrderRsp {
  // 业务状态码：0=取消并补偿成功
  int32 code = 1;
  // 响应描述信息
  string message = 2;
  // 目标订单号
  string order_id = 3;
  // 本次逆向退回的总金额（单位：分）
  int64 refunded_amount_cents = 4;
  // 本次逆向归还的库存件数
  int32 released_stock = 5;
}
`

const protoEcomAccount = `syntax = "proto3";

// 账户资产领域服务契约 (BUSINESS: demo.account.AccountService)
// @owner: asset-account-team
package demo.account;
option go_package = "code-contract/demo/pkg/proto/accountpb";

import "demo/proto/common.proto";

// 账户资产服务契约 (Service: demo.account.AccountService)
// @owner: asset-account-team
service AccountService {
  // 查询用户账户信息与可用余额
  // @owner: asset-account-team
  // @error_code 2001 ERR_ACCOUNT_USER_NOT_FOUND 目标资金账户不存在
  rpc GetBalance(GetBalanceReq) returns (GetBalanceRsp);

  // 扣减账户余额（用于下单支付环节原子扣款）
  // @owner: asset-account-team
  // @error_code 2001 ERR_ACCOUNT_USER_NOT_FOUND 目标资金账户不存在
  // @error_code 2002 ERR_ACCOUNT_BALANCE_INSUFFICIENT 账户可用余额不足，无法完成扣款
  // @error_code 2003 ERR_ACCOUNT_FROZEN 账户处于风控冻结状态
  rpc DeductBalance(DeductBalanceReq) returns (DeductBalanceRsp);

  // 退还/增加账户余额（用于下单失败补偿与取消订单逆向退费）
  // @owner: asset-account-team
  // @error_code 2001 ERR_ACCOUNT_USER_NOT_FOUND 目标资金账户不存在
  rpc RefundBalance(RefundBalanceReq) returns (RefundBalanceRsp);

  // 账户充值/资金转入
  // @owner: asset-account-team
  // @error_code 2001 ERR_ACCOUNT_USER_NOT_FOUND 目标资金账户不存在
  rpc DepositBalance(DepositBalanceReq) returns (DepositBalanceRsp);
}

// GetBalanceReq 查询用户账户余额请求
message GetBalanceReq {
  // 目标用户唯一标识（如 U1001）
  string user_id = 1;
}

// GetBalanceRsp 查询用户账户余额响应
message GetBalanceRsp {
  // 业务状态码：0=成功，2001=用户不存在
  int32 code = 1;
  // 响应描述信息
  string message = 2;
  // 用户唯一标识
  string user_id = 3;
  // 用户展示昵称
  string user_name = 4;
  // 账户当前可用余额（单位：分）
  int64 balance_cents = 5;
}

// DeductBalanceReq 扣减账户余额请求
message DeductBalanceReq {
  // 用户唯一标识
  string user_id = 1;
  // 本次扣减金额（单位：分，必须大于 0）
  int64 amount_cents = 2;
  // 关联业务订单号（用于幂等防重与对账）
  string biz_order_id = 3;
}

// DeductBalanceRsp 扣减账户余额响应
message DeductBalanceRsp {
  // 业务状态码：0=成功，2002=余额不足
  int32 code = 1;
  // 响应描述信息
  string message = 2;
  // 用户唯一标识
  string user_id = 3;
  // 扣减后的最新剩余余额（单位：分）
  int64 remaining_balance = 4;
  // 账户中心生成的记账流水号
  string tx_serial_no = 5;
}

// RefundBalanceReq 退还账户余额请求
message RefundBalanceReq {
  // 用户唯一标识
  string user_id = 1;
  // 退还金额（单位：分）
  int64 amount_cents = 2;
  // 原关联业务订单号
  string biz_order_id = 3;
  // 退款原因描述
  string reason = 4;
}

// RefundBalanceRsp 退还账户余额响应
message RefundBalanceRsp {
  // 业务状态码：0=成功
  int32 code = 1;
  // 响应描述信息
  string message = 2;
  // 用户唯一标识
  string user_id = 3;
  // 退款入账后的最新账户余额（单位：分）
  int64 current_balance = 4;
  // 退款记账流水号
  string refund_serial_no = 5;
}

// DepositBalanceReq 账户充值请求
message DepositBalanceReq {
  // 用户唯一标识
  string user_id = 1;
  // 充值金额（单位：分）
  int64 amount_cents = 2;
  // 旧版人工操作备注字段
  // @deprecated: 请改用统一审计扩展头 audit_meta 传递操作人及链路追踪信息
  string legacy_operator_note = 3 [deprecated = true];
  // 统一全链路审计扩展头
  demo.common.TraceAuditMeta audit_meta = 4;
}

// DepositBalanceRsp 账户充值响应
message DepositBalanceRsp {
  // 业务状态码：0=成功
  int32 code = 1;
  // 响应描述信息
  string message = 2;
  // 充值入账后的最新账户余额（单位：分）
  int64 current_balance = 3;
}

// AccountSettlementSnapshot 账户对账结算快照结构
// 用于演示跨业务模块（demo.account vs demo.inventory）的 AST 重复结构体识别与公共库上浮建议
// @deprecated: 与 InventorySettlementSnapshot 结构体 100% 重复，将在下个版本统一抽取上浮至 demo.common
message AccountSettlementSnapshot {
  // 关联订单流水号
  string order_id = 1;
  // 关联用户唯一标识
  string user_id = 2;
  // 结算金额（单位：分）
  int64 amount_cents = 3;
  // 对账批次凭证号
  string settlement_serial_no = 4;
  // 结算完成时间戳（Unix 秒级）
  int64 settled_at_unix = 5;
}
`

const protoEcomInventory = `syntax = "proto3";

// 库存履约领域服务契约 (BUSINESS: demo.inventory.InventoryService)
// @owner: inventory-fulfillment-team
package demo.inventory;
option go_package = "code-contract/demo/pkg/proto/inventorypb";

import "demo/proto/common.proto";

// 库存履约服务契约 (Service: demo.inventory.InventoryService)
// @owner: inventory-fulfillment-team
service InventoryService {
  // 查询 SKU 详情及实时可售库存
  // @owner: inventory-fulfillment-team
  // @error_code 3001 ERR_SKU_NOT_FOUND 商品 SKU 不存在或已下架
  rpc GetItemStock(GetItemStockReq) returns (GetItemStockRsp);

  // 阶段 1：预占/锁定库存（TCC Try 阶段）
  // @owner: inventory-fulfillment-team
  // @error_code 3001 ERR_SKU_NOT_FOUND 商品 SKU 不存在或已下架
  // @error_code 3002 ERR_STOCK_INSUFFICIENT 商品可用库存不足，拒绝锁定
  rpc LockStock(LockStockReq) returns (LockStockRsp);

  // 阶段 2A：确认扣减库存（订单支付成功，TCC Confirm 阶段）
  // @owner: inventory-fulfillment-team
  // @error_code 3003 ERR_LOCK_TOKEN_INVALID 库存锁定凭证无效或已过期释放
  rpc ConfirmStock(ConfirmStockReq) returns (ConfirmStockRsp);

  // 阶段 2B：释放预占库存（支付失败或取消订单补偿，TCC Cancel 阶段）
  // @owner: inventory-fulfillment-team
  // @error_code 3003 ERR_LOCK_TOKEN_INVALID 库存锁定凭证无效或已重复释放
  rpc ReleaseStock(ReleaseStockReq) returns (ReleaseStockRsp);
}

// GetItemStockReq 查询 SKU 库存请求
message GetItemStockReq {
  // 商品标准 SKU 编码（如 SKU-PHONE-01）
  string sku_id = 1;
}

// GetItemStockRsp 查询 SKU 库存响应
message GetItemStockRsp {
  // 业务状态码：0=成功，3001=商品不存在
  int32 code = 1;
  // 响应描述信息
  string message = 2;
  // 商品标准 SKU 编码
  string sku_id = 3;
  // 商品展示名称
  string sku_name = 4;
  // 商品销售单价（单位：分）
  int64 unit_price_cents = 5;
  // 仓库总物理库存件数
  int32 total_stock = 6;
  // 当前已预占锁定的库存件数
  int32 locked_stock = 7;
  // 实时可售库存可用量（available_stock = total_stock - locked_stock）
  int32 available_stock = 8;
}

// LockStockReq 预占锁定库存请求
message LockStockReq {
  // 商品标准 SKU 编码
  string sku_id = 1;
  // 请求锁定的购买件数
  int32 quantity = 2;
  // 关联业务订单号
  string biz_order_id = 3;
  // 全链路追踪与调用方审计扩展头
  demo.common.TraceAuditMeta audit_meta = 4;
}

// LockStockRsp 预占锁定库存响应
message LockStockRsp {
  // 业务状态码：0=锁定成功，3002=可用库存不足
  int32 code = 1;
  // 响应描述信息
  string message = 2;
  // 全局唯一锁定授权凭证 ID
  string lock_id = 3;
  // 实际锁定成功件数
  int32 locked_quantity = 4;
}

// ConfirmStockReq 确认实扣库存请求
message ConfirmStockReq {
  // 库存锁定授权凭证 ID
  string lock_id = 1;
  // 关联业务订单号
  string biz_order_id = 2;
}

// ConfirmStockRsp 确认实扣库存响应
message ConfirmStockRsp {
  // 业务状态码：0=确认扣减成功
  int32 code = 1;
  // 响应描述信息
  string message = 2;
  // 扣减后剩余总物理库存
  int32 remaining_stock = 3;
}

// ReleaseStockReq 释放预占库存请求
message ReleaseStockReq {
  // 库存锁定授权凭证 ID
  string lock_id = 1;
  // 释放补偿原因说明（如 PAYMENT_FAILED / ORDER_CANCELLED）
  string reason = 2;
}

// ReleaseStockRsp 释放预占库存响应
message ReleaseStockRsp {
  // 业务状态码：0=释放成功
  int32 code = 1;
  // 响应描述信息
  string message = 2;
  // 释放回补后的最新可售库存
  int32 restored_stock = 3;
}

// InventorySettlementSnapshot 库存履约对账结算快照结构
// 与 AccountSettlementSnapshot 结构语义高度重叠，用于静态巡检 AST 相似度检测演示
// @deprecated: 与 AccountSettlementSnapshot 结构体 100% 重复，将在下个版本统一抽取上浮至 demo.common
message InventorySettlementSnapshot {
  // 关联订单流水号
  string order_id = 1;
  // 关联用户唯一标识
  string user_id = 2;
  // 结算金额（单位：分）
  int64 amount_cents = 3;
  // 对账批次凭证号
  string settlement_serial_no = 4;
  // 结算完成时间戳（Unix 秒级）
  int64 settled_at_unix = 5;
}
`

const protoEcomAlipayChannel = `syntax = "proto3";

// 支付宝支付通道管理服务契约 (演示多实现实例独立管理接口)
// @owner: payment-alipay-team
package demo.payment.alipay;

import "demo/proto/payment.proto";

// 支付宝渠道通道运维与对账服务
// @owner: payment-alipay-team
service AlipayChannelAdminService {
  // 查询支付宝商户通道健康状态与最近一笔退款回执
  // @owner: payment-alipay-team
  // @error_code 4201 ERR_ALIPAY_GATEWAY_TIMEOUT 支付宝网关探活超时
  rpc InspectAlipayChannel(AlipayChannelInspectReq) returns (AlipayChannelInspectRsp);
}

message AlipayChannelInspectReq {
  string merchant_id = 1;
}

message AlipayChannelInspectRsp {
  int32 code = 1;
  string message = 2;
  demo.payment.RefundPayRsp last_refund_sample = 3;
}
`

const protoEcomWechatChannel = `syntax = "proto3";

// 微信支付通道管理服务契约 (演示多实现实例独立管理接口)
// @owner: payment-wechat-team
package demo.payment.wechat;

import "demo/proto/payment.proto";

// 微信支付渠道通道运维与对账服务
// @owner: payment-wechat-team
service WechatChannelAdminService {
  // 查询微信商户通道健康状态与最近一笔授权回执
  // @owner: payment-wechat-team
  // @error_code 4301 ERR_WECHAT_CERT_EXPIRED 微信商户 API 证书即将过期
  rpc InspectWechatChannel(WechatChannelInspectReq) returns (WechatChannelInspectRsp);
}

message WechatChannelInspectReq {
  string mch_id = 1;
}

message WechatChannelInspectRsp {
  int32 code = 1;
  string message = 2;
  demo.payment.AuthorizePayRsp last_auth_sample = 3;
}
`

// 刻意包含 UTF-8 BOM、CRLF、C 预处理宏、缺 syntax、重复 package、uService DSL、块内单引号无分号裸 import、多行折行、event 无返回、隐式 VoidType
const protoFabMotionImpl = "\xEF\xBB\xBF" +
	"#pragma once\r\n" +
	"#include \"legacy_macros.h\"\r\n" +
	"\r\n" +
	"// 设备晶圆台运动控制服务 (故意不以 *Service.proto 命名，验证按内容识别 uService)\r\n" +
	"// @owner: fab-motion-team\r\n" +
	"package fab.motion;\r\n" +
	"package fab.motion;\r\n" +
	"\r\n" +
	"uService StageMotionService {\r\n" +
	"  import 'StageMotionMsg.proto'\r\n" +
	"\r\n" +
	"  // 控制晶圆台移动至指定 XY 坐标\r\n" +
	"  // @error_code 6001 ERR_STAGE_OUT_OF_RANGE 目标坐标超出软限位\r\n" +
	"  interface MoveStage(MoveStageReq) return (MoveStageRsp) (timeout:5000);\r\n" +
	"\r\n" +
	"  // 多行折行签名的轴校准接口\r\n" +
	"  interface CalibrateAxis(\r\n" +
	"    CalibrateReq\r\n" +
	"  ) return (CalibrateRsp) (timeout:3000);\r\n" +
	"\r\n" +
	"  // 急停事件单向通知（无 return，转换为 google.protobuf.Empty）\r\n" +
	"  interface NotifyEmergencyStop(EmergencyStopEvt) (event);\r\n" +
	"\r\n" +
	"  // 心跳探测（隐式引用共享基座 sw.basic.VoidType）\r\n" +
	"  interface PingHealth(VoidType) return (VoidType) (timeout:1000);\r\n" +
	"}\r\n"

// 刻意缺 syntax，使用 proto2 required / default 与自定义字段扩展选项
const protoFabMotionMsg = `// 晶圆台运动控制消息体定义 (proto2 语义 + R4 自定义字段选项)
package fab.motion;

import "ltcommon/pg/OptionsExtensions.proto";

message MoveStageReq {
  required int32 axis_id = 1 [
    (ltcommon.options.cmRange) = "1..4",
    (ltcommon.options.units) = "index",
    (ltcommon.options.visibility) = "PUBLIC",
    (ltcommon.options.description) = "运动轴编号"
  ];
  required double target_x_mm = 2 [
    default = 0.0,
    (ltcommon.options.cmRange) = "-300.0..300.0",
    (ltcommon.options.units) = "mm",
    (ltcommon.options.description) = "X axis target"
  ];
  optional double speed_mm_s = 3 [default = 100];
}

message MoveStageRsp {
  required int32 code = 1;
  optional string message = 2;
  optional double actual_x_mm = 3 [(ltcommon.options.readOnly) = true];
}

message CalibrateReq {
  required int32 axis_id = 1;
  optional bool force_home = 2 [default = false];
}

message CalibrateRsp {
  required int32 code = 1;
  optional double offset_um = 2;
}

message EmergencyStopEvt {
  required string source_node = 1;
  optional int64 triggered_at_ms = 2;
}
`

const protoFabMotionTC = `syntax = "proto2";

package fab.motion;

// StageMotionTestCase 仅测试夹具使用，未被任何 uService 引用，验证 R1 闭包隔离
message StageMotionTestCase {
  optional string case_id = 1;
  optional string expect_err = 2;
}
`

const protoFabOpticsService = `// 光学对准服务 (验证去 /pg 精确边界: extpg 与 pg.proto 必须保留)
package fab.optics;

uService OpticsAlignService {
  import "fab/shared/SharedDiagMsg.proto";
  import "extpg/OpticsCoeff.proto";
  import "pg.proto";

  interface AlignLaser(OpticsAlignReq) return (OpticsAlignRsp) (timeout:2000);
}

message OpticsAlignReq {
  optional string channel_id = 1;
  optional fab.optics.ext.OpticsCoeff coeff = 2;
  optional fab.optics.PgRootMeta root_meta = 3;
  optional fab.shared.SharedDiagMsg diag = 4;
}

message OpticsAlignRsp {
  optional int32 code = 1;
  optional string status = 2;
}
`

const protoFabOpticsCoeff = `syntax = "proto2";

package fab.optics.ext;

message OpticsCoeff {
  optional double gain = 1 [default = 1.0];
  optional int32 wavelength_nm = 2;
}
`

const protoFabPGFile = `syntax = "proto2";

package fab.optics;

// 文件名本身为 pg.proto，验证 StripPGPathSegment 绝不误删末尾文件名
message PgRootMeta {
  optional string profile_name = 1;
}
`

const protoFabOpticsAlpha = `// R0 变体目录 alpha 历史副本，应被 exclude_globs 拦截
package fab.optics;
message AlphaLegacyStub { optional string id = 1; }
`

const protoFabOpticsM1 = `// R0 变体目录 m1 历史副本，应被 exclude_globs 拦截
package fab.optics;
message M1LegacyStub { optional string id = 1; }
`

const protoFabSharedDiagPrimary = `syntax = "proto2";

package fab.shared;

// SharedDiagMsg 主仓权威定义（与 sensor-copy 仓内容不等价，用于触发 symbol_diff）
message SharedDiagMsg {
  optional string diag_id = 1;
  optional double temp_celsius = 2;
  optional double laser_power_mw = 3;
}
`

const protoFabSharedDiagDivergent = `syntax = "proto2";

package fab.shared;

// SharedDiagMsg 传感器仓拷贝副本（内容不等价：含 sensor_drift_ppm 而缺失 laser_power_mw）
message SharedDiagMsg {
  optional string diag_id = 1;
  optional double temp_celsius = 2;
  optional double sensor_drift_ppm = 3;
}

message SensorOnlyDiagExtra {
  optional int32 sample_hz = 1;
}
`

const protoFabIdenticalUtil = `syntax = "proto2";

package fab.shared;

// IdenticalUtilMsg 跨仓字节级完全一致副本，用于验证 R3 content_identical = true 展示
message DiagTimestamp {
  optional int64 sec = 1;
  optional int32 nanos = 2;
}
`

const protoFabSensorService = `// 传感采集服务（刻意含错误路径 import 与缺失同目录兄弟 import，供 R7 演进验证）
package fab.sensor;

uService SensorReadService {
  import "wrong/path/OpticsCoeff.proto";

  interface ReadSensor(SensorReadReq) return (SensorReadRsp) (timeout:1500);
}
`

const protoFabSensorCalibMsg = `syntax = "proto2";

package fab.sensor;

message SensorCalibData {
  optional double zero_bias = 1;
  optional double scale_factor = 2;
}

message SensorReadReq {
  optional string sensor_id = 1;
}

message SensorReadRsp {
  optional int32 code = 1;
  optional SensorCalibData calib = 2;
}
`

const protoFabCorruptSyntax = `syntax = "proto3";

package fab.broken;

// 刻意构造不可自动修复的语法破坏，验证 Stage B QUARANTINED 隔离区与错误分布统计
message CorruptPayload {
  string broken_field = ;;;
}
`

const protoMiscHealthCheck = `syntax = "proto3";

// 遗留杂项健康检查服务（所在仓库 service_group 为空，归入 _unassigned）
// @owner: ops-platform-team
package misc.health;

service HealthCheckService {
  // 探活接口
  rpc CheckHealth(CheckHealthReq) returns (CheckHealthRsp);
}

message CheckHealthReq {
  string probe_id = 1;
}

message CheckHealthRsp {
  int32 code = 1;
  string status = 2;
}
`
