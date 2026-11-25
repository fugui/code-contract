package handlers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"code-contract/config"
	"code-contract/internal/server/dashboard"
	"code-contract/models"
	"gorm.io/datatypes"
)

func TestConfirmProto_RBAC_UnclaimedVsClaimed(t *testing.T) {
	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN 未配置，跳过隔离集成测试")
	}

	r, db := setupRouterAndDB(t)

	adminUser := createTestUserInDB(t, db, "cat_admin", config.AdminRole)
	subOwner := createTestUserInDB(t, db, "cat_sub_owner", "developer")
	modOwner := createTestUserInDB(t, db, "cat_mod_owner", "developer")
	otherUser := createTestUserInDB(t, db, "cat_other", "developer")

	adminToken := generateTestJWT(t, adminUser)
	subOwnerToken := generateTestJWT(t, subOwner)
	modOwnerToken := generateTestJWT(t, modOwner)
	otherToken := generateTestJWT(t, otherUser)

	// 创建权威 SHARED 环境与私有 PRIVATE 环境
	sharedAgent := models.Agent{Name: "cat-shared", Endpoint: "http://10.40.1.1:19001", Scope: models.ScopeShared, Status: models.AgentStatusOnline}
	privAgent := models.Agent{Name: "cat-priv", Endpoint: "http://10.40.2.1:19001", Scope: models.ScopePrivate, OwnerID: &modOwner.ID, Status: models.AgentStatusOnline}
	_ = db.Create(&sharedAgent).Error
	_ = db.Create(&privAgent).Error

	sub := models.Subsystem{Code: "SUB_CAT", Name: "目录子系统", OwnerID: &subOwner.ID}
	_ = db.Create(&sub).Error
	fg := models.FunctionalGroup{SubsystemID: sub.ID, Code: "FG_CAT", Name: "目录组", Level: 1, Path: "/1/"}
	_ = db.Create(&fg).Error
	pgrp := models.ArchProcessGroup{Code: "PGRP_CAT", Name: "目录进程组"}
	_ = db.Create(&pgrp).Error
	apShared := models.ArchProcess{GroupID: pgrp.ID, Code: "ap-cat-s", Name: "CatShared", Lifecycle: models.ArchProcLifecycleConfirmed}
	apPriv := models.ArchProcess{GroupID: pgrp.ID, Code: "prv-cat", Name: "CatPriv", Lifecycle: models.ArchProcLifecycleProvisional, OriginAgentID: &privAgent.ID}
	_ = db.Create(&apShared).Error
	_ = db.Create(&apPriv).Error

	mod := models.Module{SubsystemID: sub.ID, GroupID: fg.ID, Code: "MOD_CAT", Name: "目录模块", ArchProcessID: &apShared.ID, OwnerID: &modOwner.ID}
	_ = db.Create(&mod).Error

	procShared := models.Process{AgentID: sharedAgent.ID, ArchProcessID: apShared.ID, EffectiveListenPort: 25001, ExtraDownstreams: datatypes.JSON("[]")}
	procPriv := models.Process{AgentID: privAgent.ID, ArchProcessID: apPriv.ID, EffectiveListenPort: 25002, ExtraDownstreams: datatypes.JSON("[]")}
	_ = db.Create(&procShared).Error
	_ = db.Create(&procPriv).Error

	// 激活 .pb 操作用于启发式撮合
	schema := models.ProtoSchema{Name: "cat_pb", Version: 1, Category: models.ProtoCategoryBusiness, FileName: "cat.pb", FileSize: 128, DescriptorBytes: []byte{0x0a, 0x00}, IsActive: true, ServiceCount: 2}
	if err := db.Create(&schema).Error; err != nil {
		t.Fatalf("create cat schema: %v", err)
	}
	_ = db.Create(&models.ProtoOperation{SchemaID: schema.ID, ServiceName: "cat.v1.BillingService", ProtoPackage: "cat.v1", ProtoService: "BillingService", Operation: "Pay", IsActive: true}).Error
	_ = db.Create(&models.ProtoOperation{SchemaID: schema.ID, ServiceName: "cat.v1.ClaimedService", ProtoPackage: "cat.v1", ProtoService: "ClaimedService", Operation: "Get", IsActive: true}).Error

	now := time.Now().UTC()
	// 1. 未认领 SHARED 服务
	svcUnclaimedShared := models.ModuleService{
		ServiceName:          "BillingImpl-BillingService",
		ProtoSuggestedName:   "cat.v1.BillingService",
		ProtoMatchConfidence: models.ProtoMatchConfHeuristic,
		Source:               models.ServiceSourceCSGDiscovered,
		DiffIgnorePaths:      datatypes.JSON("[]"),
	}
	// 2. 未认领纯 PRIVATE 服务
	svcUnclaimedPriv := models.ModuleService{
		ServiceName:     "PrivOnlySvc",
		Source:          models.ServiceSourceCSGDiscovered,
		DiffIgnorePaths: datatypes.JSON("[]"),
	}
	// 3. 已认领服务
	svcClaimed := models.ModuleService{
		ModuleID:        models.Uint64Ptr(mod.ID),
		ServiceName:     "ClaimedImpl-ClaimedService",
		Source:          models.ServiceSourceManual,
		DiffIgnorePaths: datatypes.JSON("[]"),
	}
	_ = db.Create(&svcUnclaimedShared).Error
	_ = db.Create(&svcUnclaimedPriv).Error
	_ = db.Create(&svcClaimed).Error

	_ = db.Create(&models.ServicePlacement{ModuleServiceID: svcUnclaimedShared.ID, ProcessID: procShared.ID, Status: models.PlacementStatusActive, FirstSeenAt: now, LastSeenAt: now}).Error
	_ = db.Create(&models.ServicePlacement{ModuleServiceID: svcUnclaimedPriv.ID, ProcessID: procPriv.ID, Status: models.PlacementStatusActive, FirstSeenAt: now, LastSeenAt: now}).Error
	_ = db.Create(&models.ServicePlacement{ModuleServiceID: svcClaimed.ID, ProcessID: procShared.ID, Status: models.PlacementStatusActive, FirstSeenAt: now, LastSeenAt: now}).Error

	postConfirm := func(token string, svcID uint64, protoName string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"proto_service_name": protoName})
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/catalog/services/%d/confirm-proto", svcID), bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// A. 未认领 SHARED 服务：任意可见认证用户可确认
	if w := postConfirm(otherToken, svcUnclaimedShared.ID, "cat.v1.BillingService"); w.Code != http.StatusOK {
		t.Fatalf("unclaimed SHARED confirm by normal user want 200, got %d body=%s", w.Code, w.Body.String())
	}

	// B. 未认领纯 PRIVATE 服务：非 owner 普通用户 403，owner 200
	if w := postConfirm(otherToken, svcUnclaimedPriv.ID, "cat.v1.BillingService"); w.Code != http.StatusForbidden {
		t.Fatalf("unclaimed PRIVATE confirm by non-owner want 403, got %d body=%s", w.Code, w.Body.String())
	}
	if w := postConfirm(modOwnerToken, svcUnclaimedPriv.ID, "cat.v1.BillingService"); w.Code != http.StatusOK {
		t.Fatalf("unclaimed PRIVATE confirm by owner want 200, got %d body=%s", w.Code, w.Body.String())
	}

	// C. 已认领服务：非负责人普通用户 403；模块负责人 200；子系统负责人 200；管理员 200
	if w := postConfirm(otherToken, svcClaimed.ID, "cat.v1.ClaimedService"); w.Code != http.StatusForbidden {
		t.Fatalf("claimed confirm by non-owner want 403, got %d body=%s", w.Code, w.Body.String())
	}
	if w := postConfirm(modOwnerToken, svcClaimed.ID, "cat.v1.ClaimedService"); w.Code != http.StatusOK {
		t.Fatalf("claimed confirm by module owner want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if w := postConfirm(subOwnerToken, svcClaimed.ID, "cat.v1.ClaimedService"); w.Code != http.StatusOK {
		t.Fatalf("claimed confirm by subsystem owner want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if w := postConfirm(adminToken, svcClaimed.ID, "cat.v1.ClaimedService"); w.Code != http.StatusOK {
		t.Fatalf("claimed confirm by admin want 200, got %d body=%s", w.Code, w.Body.String())
	}

	// D. 验证 POST /api/catalog/correlate 与 GET /api/catalog/services 与 GET /api/dashboard/overview
	reqCorr := httptest.NewRequest(http.MethodPost, "/api/catalog/correlate", bytes.NewReader([]byte(`{}`)))
	reqCorr.Header.Set("Authorization", "Bearer "+otherToken)
	reqCorr.Header.Set("Content-Type", "application/json")
	wCorr := httptest.NewRecorder()
	r.ServeHTTP(wCorr, reqCorr)
	if wCorr.Code != http.StatusOK {
		t.Fatalf("POST /api/catalog/correlate want 200, got %d body=%s", wCorr.Code, wCorr.Body.String())
	}

	reqList := httptest.NewRequest(http.MethodGet, "/api/catalog/services", nil)
	reqList.Header.Set("Authorization", "Bearer "+otherToken)
	wList := httptest.NewRecorder()
	r.ServeHTTP(wList, reqList)
	if wList.Code != http.StatusOK {
		t.Fatalf("GET /api/catalog/services want 200, got %d body=%s", wList.Code, wList.Body.String())
	}

	reqDash := httptest.NewRequest(http.MethodGet, "/api/dashboard/overview", nil)
	reqDash.Header.Set("Authorization", "Bearer "+otherToken)
	wDash := httptest.NewRecorder()
	r.ServeHTTP(wDash, reqDash)
	if wDash.Code != http.StatusOK {
		t.Fatalf("GET /api/dashboard/overview want 200, got %d body=%s", wDash.Code, wDash.Body.String())
	}
	var dashDTO dashboard.DashboardOverviewDTO
	if err := json.Unmarshal(wDash.Body.Bytes(), &dashDTO); err != nil {
		t.Fatalf("unmarshal dashboard overview: %v", err)
	}
	if dashDTO.FiveStateRadar == nil {
		t.Fatalf("expected FiveStateRadar populated in /api/dashboard/overview")
	}
	expectedTotal := dashDTO.HealthScore.ArchSubScore + dashDTO.HealthScore.AssetSubScore + dashDTO.HealthScore.RegressionSubScore
	if dashDTO.HealthScore.Total != expectedTotal {
		t.Fatalf("40+30+30 health score formula broken: total=%d sum=%d", dashDTO.HealthScore.Total, expectedTotal)
	}
}
