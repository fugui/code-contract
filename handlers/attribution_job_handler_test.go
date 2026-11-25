package handlers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"code-contract/config"
	"code-contract/internal/server/topology"
	"code-contract/models"
	"gorm.io/datatypes"
)

func TestAttributionJob_API_GetAndCancel_RBAC_AndTerminal409(t *testing.T) {
	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN 未配置，跳过隔离集成测试")
	}
	r, db := setupRouterAndDB(t)

	admin := createTestUserInDB(t, db, "ajob_admin", config.AdminRole)
	creator := createTestUserInDB(t, db, "ajob_creator", "developer")
	other := createTestUserInDB(t, db, "ajob_other", "developer")

	adminTok := generateTestJWT(t, admin)
	creatorTok := generateTestJWT(t, creator)
	otherTok := generateTestJWT(t, other)

	job := models.AttributionJob{
		JobType:   models.AttrJobTypeReattribute,
		ScopeJSON: datatypes.JSON(`{"service_names":["demo.v1.Svc"],"matched_count":2}`),
		Status:    models.AttrJobStatusRunning,
		Processed: 1,
		Total:     4,
		CreatedBy: creator.ID,
	}
	if err := db.Create(&job).Error; err != nil {
		t.Fatalf("create AttributionJob: %v", err)
	}

	// GET 不存在 -> 404
	req404 := httptest.NewRequest(http.MethodGet, "/api/attribution-jobs/9999999", nil)
	req404.Header.Set("Authorization", "Bearer "+creatorTok)
	w404 := httptest.NewRecorder()
	r.ServeHTTP(w404, req404)
	if w404.Code != http.StatusNotFound {
		t.Fatalf("GET 不存在作业期望 404，实际 %d", w404.Code)
	}

	// GET 正常 -> 200
	reqGet := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/attribution-jobs/%d", job.ID), nil)
	reqGet.Header.Set("Authorization", "Bearer "+otherTok)
	wGet := httptest.NewRecorder()
	r.ServeHTTP(wGet, reqGet)
	if wGet.Code != http.StatusOK {
		t.Fatalf("GET 作业期望 200，实际 %d body=%s", wGet.Code, wGet.Body.String())
	}
	var dto topology.AttributionJobDTO
	if err := json.Unmarshal(wGet.Body.Bytes(), &dto); err != nil {
		t.Fatalf("unmarshal AttributionJobDTO: %v", err)
	}
	if dto.Processed != 1 || dto.Total != 4 || dto.MatchedCount != 2 || len(dto.ServiceNames) != 1 {
		t.Fatalf("GET 返回 DTO 不符: %+v", dto)
	}

	// 他人尝试取消 -> 403
	reqForb := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/attribution-jobs/%d/cancel", job.ID), nil)
	reqForb.Header.Set("Authorization", "Bearer "+otherTok)
	wForb := httptest.NewRecorder()
	r.ServeHTTP(wForb, reqForb)
	if wForb.Code != http.StatusForbidden {
		t.Fatalf("他人取消期望 403，实际 %d body=%s", wForb.Code, wForb.Body.String())
	}

	// 创建人取消 -> 200 CANCELLED
	reqCancel := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/attribution-jobs/%d/cancel", job.ID), nil)
	reqCancel.Header.Set("Authorization", "Bearer "+creatorTok)
	wCancel := httptest.NewRecorder()
	r.ServeHTTP(wCancel, reqCancel)
	if wCancel.Code != http.StatusOK {
		t.Fatalf("创建人取消期望 200，实际 %d body=%s", wCancel.Code, wCancel.Body.String())
	}
	_ = json.Unmarshal(wCancel.Body.Bytes(), &dto)
	if dto.Status != models.AttrJobStatusCancelled {
		t.Fatalf("取消后状态期望 CANCELLED，实际 %s", dto.Status)
	}

	// 终态再次取消（即使管理员）-> 409
	reqTerm := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/attribution-jobs/%d/cancel", job.ID), nil)
	reqTerm.Header.Set("Authorization", "Bearer "+adminTok)
	wTerm := httptest.NewRecorder()
	r.ServeHTTP(wTerm, reqTerm)
	if wTerm.Code != http.StatusConflict {
		t.Fatalf("终态取消期望 409，实际 %d body=%s", wTerm.Code, wTerm.Body.String())
	}
}
