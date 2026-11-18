package decoder

import (
	"os"
	"testing"

	"code-contract/models"
	"gorm.io/datatypes"
)

func TestDecoderRegistry_64BServiceAliasesAndPidSuffixNormalization(t *testing.T) {
	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN 未配置，跳过隔离数据库测试")
	}

	db := setupIsolatedDecoderDB(t)

	reg, err := NewRegistry(db)
	if err != nil {
		t.Fatalf("NewRegistry init failed: %v", err)
	}

	// Upload & activate .pb schema defining order.v1.OrderService.CreateOrder
	descBytes := buildTestOrderDescriptorBytes(t, "order.v1", "OrderService", "CreateOrder", false)
	if _, _, _, err := reg.UploadSchema(UploadSchemaInput{
		Name:         "order_v1_schema",
		VersionTag:   "v1.0.0",
		FileName:     "order.pb",
		RawBytes:     descBytes,
		AutoActivate: true,
		OperatorID:   1,
	}); err != nil {
		t.Fatalf("UploadSchema failed: %v", err)
	}

	sub := models.Subsystem{Code: "SUB_DEC", Name: "DecoderSub"}
	if err := db.Create(&sub).Error; err != nil {
		t.Fatalf("create sub failed: %v", err)
	}
	grp := models.FunctionalGroup{SubsystemID: sub.ID, Code: "GRP_DEC", Name: "DecoderGrp"}
	if err := db.Create(&grp).Error; err != nil {
		t.Fatalf("create grp failed: %v", err)
	}
	mod := models.Module{SubsystemID: sub.ID, GroupID: grp.ID, Code: "MOD_DEC", Name: "DecoderMod"}
	if err := db.Create(&mod).Error; err != nil {
		t.Fatalf("create mod failed: %v", err)
	}

	// CSG runtime service name uses 64B Class-Service format "OrderServiceImpl-OrderService",
	// aliased via ProtoServiceName to "order.v1.OrderService".
	msAlias := models.ModuleService{
		ModuleID:         mod.ID,
		ServiceName:      "OrderServiceImpl-OrderService",
		ProtoServiceName: "order.v1.OrderService",
		DiffIgnorePaths:  datatypes.JSON(`["$.trace_id"]`),
	}
	// Another service registered with "-0" instance index receives live traffic with PID suffix "-12345".
	msPid := models.ModuleService{
		ModuleID:         mod.ID,
		ServiceName:      "OrderService-0",
		ProtoServiceName: "order.v1.OrderService",
		DiffIgnorePaths:  datatypes.JSON(`["$.server_ts"]`),
	}
	if err := db.Create(&msAlias).Error; err != nil {
		t.Fatalf("create msAlias failed: %v", err)
	}
	if err := db.Create(&msPid).Error; err != nil {
		t.Fatalf("create msPid failed: %v", err)
	}

	if err := reg.Reload(); err != nil {
		t.Fatalf("reg.Reload failed: %v", err)
	}

	// 1. Direct lookup via CSG 64B service_name -> maps via ProtoServiceName to order.v1.OrderService
	op1 := reg.LookupOperation("OrderServiceImpl-OrderService", "CreateOrder")
	if op1 == nil {
		t.Fatalf("expected LookupOperation to resolve CSG 64B alias OrderServiceImpl-OrderService")
	}
	if op1.ServiceName != "OrderServiceImpl-OrderService" || op1.Operation != "CreateOrder" {
		t.Fatalf("unexpected resolved operation metadata: %+v", op1)
	}
	if len(op1.ServiceIgnorePaths) != 1 || op1.ServiceIgnorePaths[0] != "$.trace_id" {
		t.Fatalf("expected ServiceIgnorePaths for CSG service, got %+v", op1.ServiceIgnorePaths)
	}

	// 2. Runtime CSG frame with PID suffix "OrderService-0-12345" normalizes to "OrderService-0" -> "order.v1.OrderService"
	op2 := reg.LookupOperation("OrderService-0-12345", "CreateOrder")
	if op2 == nil {
		t.Fatalf("expected LookupOperation to resolve PID-suffixed OrderService-0-12345")
	}
	if op2.ServiceName != "OrderService-0-12345" || len(op2.ServiceIgnorePaths) != 1 || op2.ServiceIgnorePaths[0] != "$.server_ts" {
		t.Fatalf("unexpected PID-normalized op2: %+v", op2)
	}
}
