package topology

import (
	"testing"
	"time"

	"code-common/backend/testdb"
	"code-contract/config"
	"code-contract/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// inboxEnv M32 收件箱/认领/依赖合成/归因回补测试共享夹具
type inboxEnv struct {
	db *gorm.DB

	adminUser, subOwner, modOwner, otherUser *models.User

	sharedAgent, privAgent models.Agent
	sub                    models.Subsystem
	fg                     models.FunctionalGroup
	pgrp                   models.ArchProcessGroup
	ap1, ap2, apPriv       models.ArchProcess
	proc1, proc2, procPriv models.Process
	modA, modB             models.Module // 初始未绑进程，modA 由 modOwner 负责
	modC                   models.Module // 已绑 ap2
	libMod                 models.Module // CONTRACT_LIB
}

func newInboxEnv(t *testing.T) *inboxEnv {
	t.Helper()
	db := testdb.SetupIsolatedDB(t, config.ServiceName)
	if err := models.SetupWithDB(db); err != nil {
		t.Fatalf("SetupWithDB 失败: %v", err)
	}
	e := &inboxEnv{db: db}
	mkUser := func(name string) *models.User {
		u := &models.User{Username: name, Name: name, Email: name + "@example.com", Password: "x", Roles: datatypes.JSON(`["developer"]`)}
		if err := db.Create(u).Error; err != nil {
			t.Fatalf("创建用户 %s 失败: %v", name, err)
		}
		return u
	}
	e.adminUser = mkUser("m32_admin")
	e.subOwner = mkUser("m32_sub_owner")
	e.modOwner = mkUser("m32_mod_owner")
	e.otherUser = mkUser("m32_other")

	e.sharedAgent = models.Agent{Name: "m32-shared", Endpoint: "10.50.1.1:19001", Scope: models.ScopeShared, Status: models.AgentStatusOnline}
	e.privAgent = models.Agent{Name: "m32-priv", Endpoint: "10.50.2.1:19001", Scope: models.ScopePrivate, OwnerID: &e.modOwner.ID, Status: models.AgentStatusOnline}
	mustCreate(t, db, &e.sharedAgent)
	mustCreate(t, db, &e.privAgent)

	e.sub = models.Subsystem{Code: "M32_SUB", Name: "M32 子系统", OwnerID: &e.subOwner.ID}
	mustCreate(t, db, &e.sub)
	e.fg = models.FunctionalGroup{SubsystemID: e.sub.ID, Code: "M32_FG", Name: "M32 功能组", Level: 1, Path: "/1/"}
	mustCreate(t, db, &e.fg)
	e.pgrp = models.ArchProcessGroup{Code: "PGRP_M32", Name: "M32 进程组"}
	mustCreate(t, db, &e.pgrp)

	e.ap1 = models.ArchProcess{GroupID: e.pgrp.ID, Code: "ap-m32-1", Name: "M32 进程1", Lifecycle: models.ArchProcLifecycleConfirmed}
	e.ap2 = models.ArchProcess{GroupID: e.pgrp.ID, Code: "ap-m32-2", Name: "M32 进程2", Lifecycle: models.ArchProcLifecycleConfirmed}
	e.apPriv = models.ArchProcess{GroupID: e.pgrp.ID, Code: "prv-m32", Name: "M32 私有进程", Lifecycle: models.ArchProcLifecycleProvisional, OriginAgentID: &e.privAgent.ID}
	mustCreate(t, db, &e.ap1)
	mustCreate(t, db, &e.ap2)
	mustCreate(t, db, &e.apPriv)

	mkProc := func(agentID, apID uint64, port int) models.Process {
		p := models.Process{
			AgentID: agentID, ArchProcessID: apID, EffectiveListenPort: port,
			ExtraDownstreams: datatypes.JSON("[]"), EnrollSource: models.EnrollSourceDiscovery,
			ProbeDesiredState: models.ProbeDesiredStateDisarmed, ProbeStatus: models.ProbeStatusDisarmed,
		}
		mustCreate(t, db, &p)
		return p
	}
	e.proc1 = mkProc(e.sharedAgent.ID, e.ap1.ID, 31001)
	e.proc2 = mkProc(e.sharedAgent.ID, e.ap2.ID, 31002)
	e.procPriv = mkProc(e.privAgent.ID, e.apPriv.ID, 31003)

	e.modA = models.Module{SubsystemID: e.sub.ID, GroupID: e.fg.ID, Code: "M32_MOD_A", Name: "模块A", OwnerID: &e.modOwner.ID}
	e.modB = models.Module{SubsystemID: e.sub.ID, GroupID: e.fg.ID, Code: "M32_MOD_B", Name: "模块B", OwnerID: &e.modOwner.ID}
	e.modC = models.Module{SubsystemID: e.sub.ID, GroupID: e.fg.ID, Code: "M32_MOD_C", Name: "模块C", ArchProcessID: &e.ap2.ID, OwnerID: &e.modOwner.ID}
	e.libMod = models.Module{SubsystemID: e.sub.ID, GroupID: e.fg.ID, Code: "M32_LIB", Name: "接口库", ModuleKind: models.ModuleKindContractLib}
	mustCreate(t, db, &e.modA)
	mustCreate(t, db, &e.modB)
	mustCreate(t, db, &e.modC)
	mustCreate(t, db, &e.libMod)
	return e
}

func mustCreate(t *testing.T, db *gorm.DB, v interface{}) {
	t.Helper()
	if err := db.Create(v).Error; err != nil {
		t.Fatalf("创建夹具失败 %T: %v", v, err)
	}
}

// newUnclaimedSvc 创建未认领身份 (module_id=NULL, source=CSG_DISCOVERED)
func (e *inboxEnv) newUnclaimedSvc(t *testing.T, name string) models.ModuleService {
	t.Helper()
	svc := models.ModuleService{ServiceName: name, Source: models.ServiceSourceCSGDiscovered, DiffIgnorePaths: datatypes.JSON("[]")}
	mustCreate(t, e.db, &svc)
	return svc
}

// newClaimedSvc 创建已认领手工服务
func (e *inboxEnv) newClaimedSvc(t *testing.T, name string, moduleID uint64) models.ModuleService {
	t.Helper()
	svc := models.ModuleService{ModuleID: models.Uint64Ptr(moduleID), ServiceName: name, Source: models.ServiceSourceManual, DiffIgnorePaths: datatypes.JSON("[]")}
	mustCreate(t, e.db, &svc)
	return svc
}

func (e *inboxEnv) place(t *testing.T, svcID, procID uint64, status string) models.ServicePlacement {
	t.Helper()
	now := time.Now().UTC()
	pl := models.ServicePlacement{
		ModuleServiceID: svcID, ProcessID: procID, RawServiceName: "raw", Endpoints: datatypes.JSON(`["tcp://10.50.1.1:31001"]`),
		ObservedTransport: models.ObservedTransportTCP, InstanceCount: 1, Status: status, FirstSeenAt: now, LastSeenAt: now,
	}
	mustCreate(t, e.db, &pl)
	return pl
}

// subscribe 建立消费方进程对服务身份的订阅边 (ProcessDependency)
func (e *inboxEnv) subscribe(t *testing.T, consumerProcID, svcID uint64, svcName, status string) models.ProcessDependency {
	t.Helper()
	now := time.Now().UTC()
	dep := models.ProcessDependency{
		ConsumerProcessID: consumerProcID, ServiceName: svcName, ServiceID: models.Uint64Ptr(svcID),
		ProviderKind: models.ProviderKindLocalEnrolled, Source: models.ProcDepSourceCSGSub, Status: status,
		FirstSeenAt: now, LastSeenAt: now,
	}
	mustCreate(t, e.db, &dep)
	return dep
}
