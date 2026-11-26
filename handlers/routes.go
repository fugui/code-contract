package handlers

import (
	commonAudit "code-common/backend/audit"
	commonAuth "code-common/backend/auth"
	"code-contract/config"
	"code-contract/models"

	"github.com/gin-gonic/gin"
)

// RejectQueryTokenMiddleware 严格拦截在 URL Query 中传递敏感 Token 的行为
func RejectQueryTokenMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Query("token") != "" {
			c.AbortWithStatusJSON(401, gin.H{"error": "严禁在 URL Query 参数中传递 Token，必须使用 Authorization: Bearer 标头"})
			return
		}
		c.Next()
	}
}

// BuildAuthMiddleware 构建统一 JWT 鉴权中间件（支持纯 Claims 模式与 DB 关联模式）
func BuildAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		commonAuth.AuthMiddleware(commonAuth.AuthConfig{
			JWTSecretGetter: func() string { return config.AppConfig.Auth.JWTSecret },
			DB:              models.DB,
			PreloadAssocs:   []string{"Department"},
		})(c)
	}
}

// RegisterAPIRoutes 集中注册所有开放路由、受保护业务路由与模块管理员路由
func RegisterAPIRoutes(r *gin.Engine) {
	// 1. 免密开放路由 (Unprotected)
	openAPI := r.Group("/api")
	{
		openAPI.GET("/health", HealthCheck)
		openAPI.GET("/auth/config", GetAuthConfig)
		openAPI.POST("/login", Login)
		openAPI.GET("/oauth2/authorize", StartOAuth2Flow)
		openAPI.GET("/oauth2/callback", OAuth2Callback)
		openAPI.GET("/agent/download", DownloadAgentBinary)
		// M26: GitLab / GitHub MR Webhook 回调入口（常量时间 Secret 验签）
		openAPI.POST("/webhooks/git-mr/:repoId", HandleGitMRWebhook)
		openAPI.POST("/webhooks/git-mr", HandleGitMRWebhook)
		// ADR-26: Git Push 增量同步 Webhook（常量时间验签 + SyncScheduler 调度 + commonAudit.Skip）
		openAPI.POST("/webhooks/git-sync/:repoId", HandleGitSyncWebhook)
	}

	// 2. 受保护业务路由 (Protected - 需携带合法平台 JWT)
	protected := r.Group("/api")
	protected.Use(RejectQueryTokenMiddleware())
	protected.Use(BuildAuthMiddleware())
	protected.Use(commonAudit.Middleware(config.ServiceName))
	{
		protected.GET("/me", GetMe)
		protected.PATCH("/password", UpdatePassword)
		protected.GET("/users", SearchUsers)

		// 2.1 平台级契约治理全景大盘 (F-24, ADR-14)
		protected.GET("/dashboard/overview", GetDashboardOverview)

		requireAdmin := commonAuth.RequireAdmin(config.AdminRole)

		// 3. 模块管理员专属路由 (Settings & M30b Governance)
		admin := protected.Group("/admin")
		admin.Use(requireAdmin)
		{
			admin.GET("/settings", GetModuleSettings)
			admin.PUT("/settings", UpdateModuleSettings)
			admin.POST("/ci-token/rotate", RotateCIToken)
			admin.DELETE("/ci-token", RevokeCIToken)
			admin.POST("/arch-processes/:id/promote", PromoteArchProcess)
			admin.POST("/migrations/csg-placeholder-modules", MigrateCSGPlaceholderModulesHandler)
			admin.POST("/noise-rules/append", AppendGlobalNoiseRuleHandler)
		}

		// 4. 三级架构元素与静态拓扑域 (F-01, F-02, F-04)
		protected.GET("/arch/subsystems", ListSubsystems)
		protected.POST("/arch/subsystems", requireAdmin, CreateSubsystem)
		protected.PUT("/arch/subsystems/:id", requireAdmin, UpdateSubsystem)
		protected.DELETE("/arch/subsystems/:id", requireAdmin, DeleteSubsystem)

		protected.GET("/arch/functional-groups/tree", GetFunctionalGroupTree)
		protected.POST("/arch/functional-groups", requireAdmin, CreateFunctionalGroup)
		protected.PUT("/arch/functional-groups/:id", requireAdmin, UpdateFunctionalGroup)
		protected.PATCH("/arch/functional-groups/:id/move", requireAdmin, MoveFunctionalGroup)
		protected.DELETE("/arch/functional-groups/:id", requireAdmin, DeleteFunctionalGroup)

		protected.GET("/arch/modules", ListModules)
		protected.GET("/arch/modules/:id", GetModule)
		protected.POST("/arch/modules", requireAdmin, CreateModule)
		protected.PUT("/arch/modules/:id", requireAdmin, UpdateModule)
		protected.DELETE("/arch/modules/:id", requireAdmin, DeleteModule)
		protected.POST("/arch/modules/:id/process-binding", requireAdmin, BindModuleProcess)

		protected.GET("/arch/modules/:id/services", ListModuleServices)
		protected.POST("/arch/modules/:id/services", requireAdmin, CreateModuleService)
		protected.PUT("/arch/module-services/:serviceId", requireAdmin, UpdateModuleService)
		protected.DELETE("/arch/module-services/:serviceId", requireAdmin, DeleteModuleService)

		protected.GET("/arch/modules/:id/dependencies", ListModuleDependencies)
		protected.POST("/arch/modules/:id/dependencies", requireAdmin, CreateModuleDependency)
		protected.DELETE("/arch/module-dependencies/:depId", requireAdmin, DeleteModuleDependency)

		protected.GET("/arch/services/available", ListAvailableServices)
		protected.GET("/arch/proto-services/claimable", ListClaimableProtoServices)

		// 4.0.1 M32 架构收件箱、认领/拆分/取消认领、逻辑依赖反向审批与归因回补作业 (ADR-24, F-39)
		protected.GET("/arch/inbox", GetArchInbox)
		protected.POST("/arch/services/claim", ClaimArchServices)
		protected.POST("/arch/services/split-multi-impl", requireAdmin, SplitMultiImplServices)
		protected.POST("/arch/services/unclaim", UnclaimArchService)
		protected.POST("/arch/dependency-suggestions/decide", requireAdmin, DecideArchDependencySuggestion)
		protected.GET("/attribution-jobs/:id", GetAttributionJobHandler)
		protected.POST("/attribution-jobs/:id/cancel", CancelAttributionJobHandler)

		// 4.0 多实现服务契约域 (ADR-16, M20)
		protected.GET("/arch/service-contracts", ListServiceContracts)
		protected.GET("/arch/service-contracts/:id", GetServiceContract)
		protected.POST("/arch/service-contracts", requireAdmin, CreateServiceContract)
		protected.PUT("/arch/service-contracts/:id", requireAdmin, UpdateServiceContract)
		protected.DELETE("/arch/service-contracts/:id", requireAdmin, DeleteServiceContract)
		protected.POST("/arch/service-contracts/convert", requireAdmin, ConvertExclusiveToMultiImplContract)
		protected.POST("/arch/service-contracts/:id/attach", requireAdmin, AttachExclusiveToMultiImplContract)

		// 4.1 物理进程蓝图域 (ADR-09, M10)
		protected.GET("/arch/process-groups", ListArchProcessGroups)
		protected.POST("/arch/process-groups", requireAdmin, CreateArchProcessGroup)
		protected.PUT("/arch/process-groups/:id", requireAdmin, UpdateArchProcessGroup)
		protected.DELETE("/arch/process-groups/:id", requireAdmin, DeleteArchProcessGroup)

		protected.GET("/arch/processes/tree", GetArchProcessTree)
		protected.POST("/arch/processes/batch-move", requireAdmin, BatchMoveArchProcesses)
		protected.GET("/arch/processes", ListArchProcesses)
		protected.GET("/arch/processes/:id", GetArchProcess)
		protected.POST("/arch/processes", requireAdmin, CreateArchProcess)
		protected.PUT("/arch/processes/:id", requireAdmin, UpdateArchProcess)
		protected.DELETE("/arch/processes/:id", requireAdmin, DeleteArchProcess)

		protected.GET("/arch/topology/graph", GzipMiddleware(), GetTopologyGraph)
		protected.GET("/arch/topology/static", GetStaticTopology)

		// 4.2 拓扑画布双层布局与个人视图书签域 (F-04.3, ADR-15)
		protected.GET("/arch/topology/layouts", ListTopologyLayouts)
		protected.GET("/arch/topology/layouts/personal/:id", GetPersonalTopologyLayout)
		protected.PUT("/arch/topology/layouts/canonical", requireAdmin, UpsertCanonicalTopologyLayout)
		protected.DELETE("/arch/topology/layouts/canonical", requireAdmin, ResetCanonicalTopologyLayout)
		protected.POST("/arch/topology/layouts/personal", CreatePersonalTopologyLayout)
		protected.PUT("/arch/topology/layouts/personal/:id", UpdatePersonalTopologyLayout)
		protected.DELETE("/arch/topology/layouts/personal/:id", DeletePersonalTopologyLayout)

		// 5. Protobuf 描述集与方法映射域 (F-03, F-03.5~F-03.7)
		protected.GET("/proto-schemas", ListProtoSchemas)
		protected.GET("/proto-schemas/dependencies-graph", GetProtoDependenciesGraph)
		protected.POST("/proto-schemas/batch-activate", requireAdmin, BatchActivateProtoSchemas)
		protected.GET("/proto-schemas/:id/download", DownloadProtoSchema)
		protected.GET("/proto-schemas/:id/summary", GetProtoSchemaSummary)
		protected.GET("/proto-schemas/:id/sources", GetProtoSchemaSources)
		protected.POST("/proto-schemas/compile", requireAdmin, CompileProtoSchema)
		protected.POST("/proto-schemas", requireAdmin, UploadProtoSchema)
		protected.PATCH("/proto-schemas/:id", requireAdmin, UpdateProtoSchema)
		protected.POST("/proto-schemas/:id/activate", requireAdmin, ActivateProtoSchema)
		protected.POST("/proto-schemas/:id/deactivate", requireAdmin, DeactivateProtoSchema)
		protected.DELETE("/proto-schemas/:id", requireAdmin, DeleteProtoSchema)
		protected.GET("/proto-docs/tree", GetProtoDocsTree)
		protected.GET("/proto-docs/services/:serviceName", GetProtoServiceDocDetail)
		protected.GET("/proto-docs/export", ExportProtoDocs)
		protected.GET("/proto-files", ListProtoFiles)
		protected.GET("/proto-files/tree", GetProtoFilesTreeHandler)
		protected.GET("/proto-files/:id/content", GetProtoFileContent)
		protected.GET("/proto-files/:id/versions", ListProtoFileVersions)
		protected.GET("/proto-files/:id/diff", GetProtoFileDiff)
		protected.GET("/proto-services", ListProtoServices)

		// ADR-26: 跨仓导入权威归属与符号冲突仲裁
		protected.GET("/proto-import-owners", ListProtoImportOwnersHandler)
		protected.PUT("/proto-import-owners", requireAdmin, UpsertProtoImportOwnerHandler)
		protected.POST("/proto-import-owners", requireAdmin, UpsertProtoImportOwnerHandler)
		protected.DELETE("/proto-import-owners/:id", requireAdmin, DeleteProtoImportOwnerHandler)
		protected.GET("/proto-conflicts", ListProtoConflictsHandler)
		protected.POST("/proto-conflicts/resolve", requireAdmin, ResolveProtoConflictHandler)

		// 5.0 统一契约服务目录、三级 .pb 撮合与五态健康雷达 (ADR-24, M31)
		protected.GET("/catalog/services", ListCatalogServices)
		protected.POST("/catalog/correlate", CorrelateCatalogServicesHandler)
		protected.POST("/catalog/services/:id/confirm-proto", ConfirmCatalogServiceProtoHandler)

		// 5.0 Git 契约仓、Monorepo Bundles、平台共享导入与一致性对账域 (F-21, F-28, F-29, M25, ADR-26)
		protected.GET("/git-repos/platform-discover", DiscoverPlatformReposHandler)
		protected.POST("/git-repos/platform-import", requireAdmin, ImportPlatformReposHandler)
		protected.POST("/git-repos/batch-sync", requireAdmin, BatchSyncGitReposHandler)
		protected.GET("/git-repos/baseline-report", GetGitReposBaselineReportHandler)
		protected.GET("/git-repos/arch-suggestions", GetGitReposArchSuggestionsHandler)
		protected.GET("/git-repos", ListGitRepos)
		protected.POST("/git-repos", requireAdmin, CreateGitRepo)
		protected.PUT("/git-repos/:id", requireAdmin, UpdateGitRepo)
		protected.DELETE("/git-repos/:id", requireAdmin, DeleteGitRepo)
		protected.POST("/git-repos/:id/link-platform", requireAdmin, LinkPlatformRepoHandler)
		protected.POST("/git-repos/:id/re-normalize", requireAdmin, ReNormalizeGitRepoHandler)
		protected.POST("/git-repos/:id/bundles", requireAdmin, CreateGitRepoBundle)
		protected.PUT("/git-repos/:id/bundles/:bundleId", requireAdmin, UpdateGitRepoBundle)
		protected.DELETE("/git-repos/:id/bundles/:bundleId", requireAdmin, DeleteGitRepoBundle)
		protected.POST("/git-repos/:id/sync", requireAdmin, TriggerGitRepoSync)
		protected.GET("/git-repos/:id/consistency", GetGitRepoConsistency)
		protected.GET("/git-repos/:id/branches", ListGitRepoBranches)
		protected.GET("/proto-operations", ListProtoOperations)
		protected.GET("/proto-operations/:id/schema-tree", GetOperationSchemaTree)
		protected.PUT("/proto-operations/:id/ignore-paths", requireAdmin, UpdateOperationIgnorePaths)

		// 5.1 Protobuf 静态检查规则、标准词库与检查报告域 (F-16, F-30, M24)
		protected.GET("/check-rules", ListCheckRules)
		protected.POST("/check-rules", requireAdmin, CreateCheckRule)
		protected.PUT("/check-rules/:id", requireAdmin, UpdateCheckRule)
		protected.DELETE("/check-rules/:id", requireAdmin, DeleteCheckRule)

		protected.GET("/vocabulary-terms", ListVocabularyTerms)
		protected.POST("/vocabulary-terms", requireAdmin, CreateVocabularyTerm)
		protected.PUT("/vocabulary-terms/:id", requireAdmin, UpdateVocabularyTerm)
		protected.DELETE("/vocabulary-terms/:id", requireAdmin, DeleteVocabularyTerm)

		protected.POST("/check-reports/scan", TriggerCheckReportScan)
		protected.GET("/check-reports", ListCheckReports)
		protected.GET("/check-reports/:id", GetCheckReportDetail)

		// 5.2 Git MR / PR 自动化契约门禁与站内收件箱域 (F-31, F-32, M26)
		protected.GET("/mr-check-runs", ListMRCheckRuns)
		protected.GET("/mr-check-runs/:id", GetMRCheckRunDetail)
		protected.POST("/mr-check-runs/:id/override", requireAdmin, OverrideMRCheckRun)
		protected.POST("/mr-check-runs/:id/rerun", requireAdmin, RerunMRCheckRun)

		protected.GET("/notifications", ListNotifications)
		protected.GET("/notifications/unread-count", GetUnreadNotificationCount)
		protected.POST("/notifications/:id/read", MarkNotificationRead)
		protected.POST("/notifications/read-all", MarkAllNotificationsRead)

		// 6. 公私域 Agent 纳管与排他租约管控域 (F-06, F-08)
		protected.GET("/agents", ListAgents)
		protected.GET("/agents/:id", GetAgent)
		protected.POST("/agents", RegisterAgent)
		protected.PUT("/agents/:id", UpdateAgent)
		protected.DELETE("/agents/:id", DeleteAgent)
		protected.POST("/agents/:id/handshake", HandshakeAgent)
		protected.GET("/agents/:id/discover-processes", DiscoverAgentProcesses)
		protected.GET("/agents/:id/csg/topology", GetAgentCSGTopology)

		// 6.1 现网发现工作台、持久化快照与双模 Diff 域 (ADR-24, M30a, M30b, M31, F-36~F-38)
		protected.GET("/discovery/live", GetDiscoveryLive)
		protected.POST("/discovery/enroll", EnrollDiscoveryProcesses)
		protected.POST("/discovery/quick-record", QuickRecordDiscovery)
		protected.POST("/discovery/simulation-plan", BuildDiscoverySimulationPlan)
		protected.GET("/discovery/physical", GetDiscoveryPhysical)
		protected.GET("/discovery/snapshots", ListDiscoverySnapshots)
		protected.POST("/discovery/snapshots", CreateDiscoverySnapshot)
		protected.GET("/discovery/snapshots/diff", DiffDiscoverySnapshots)
		protected.PATCH("/discovery/snapshots/:id", UpdateDiscoverySnapshot)
		protected.DELETE("/discovery/snapshots/:id", DeleteDiscoverySnapshot)
		protected.POST("/agents/:id/lease/force-unlock", requireAdmin, ForceUnlockAgentLease)
		protected.PUT("/agents/:id/process-mounts", BatchUpdateProcessMounts)
		protected.POST("/agents/:id/probes/disarm-all", DisarmAllProbes)
		protected.POST("/agents/:id/probes/arm-all", ArmAllProbes)
		protected.POST("/agents/:id/probes/rearm", RearmAgentProbes)

		// 7. 业务进程部署映射域 (F-02)
		protected.GET("/processes", ListProcesses)
		protected.GET("/processes/:id", GetProcess)
		protected.POST("/processes", CreateProcess)
		protected.PUT("/processes/:id", UpdateProcess)
		protected.DELETE("/processes/:id", DeleteProcess)
		protected.GET("/processes/:id/downstreams", GetProcessDownstreams)
		protected.POST("/processes/:id/disarm", DisarmProcessProbe)
		protected.POST("/processes/:id/arm", ArmProcessProbe)

		// 8. 协同录制与架构拓扑域 (F-05, F-11, F-16)
		protected.GET("/recording-sessions", ListRecordingSessions)
		protected.GET("/recording-sessions/:id", GetRecordingSession)
		protected.POST("/recording-sessions", StartRecordingSession)
		protected.POST("/recording-sessions/:id/stop", StopRecordingSession)
		protected.DELETE("/recording-sessions/:id/processes/:processId", RemoveProcessFromSession)
		protected.PATCH("/recording-sessions/:id/publish", PublishRecordingSession)
		protected.DELETE("/recording-sessions/:id", DeleteRecordingSession)
		protected.GET("/recording-sessions/:id/consistency-report", GetConsistencyReport)
		protected.GET("/recording-sessions/:id/transactions", GetRecordingSessionTransactions)
		protected.GET("/recording-sessions/:id/transactions/:rootRecordId/sequence", GetRecordingSessionTransactionSequence)

		// 9. 流量检索与解剖域 (F-03, F-11)
		protected.GET("/noise-rules", GetNoiseRulesHandler)
		protected.GET("/traffic-records", ListTrafficRecords)
		protected.GET("/traffic-records/:id/anatomy", GetTrafficRecordAnatomy)
		protected.GET("/traffic-records/:id/replay-closure", GetTrafficRecordReplayClosure)
		protected.POST("/traffic-records/:id/redecode", RedecodeTrafficRecord)

		// 10. 跨 Agent 同构离线重放与双层回归 Diff 域 (F-12, F-14)
		protected.GET("/replay-tasks", ListReplayTasks)
		protected.GET("/replay-tasks/:id", GetReplayTask)
		protected.POST("/replay-tasks", CreateReplayTask)
		protected.POST("/replay-tasks/:id/cancel", CancelReplayTask)
		protected.GET("/replay-tasks/:id/items", ListReplayItems)
		protected.GET("/replay-tasks/:id/items/:itemId", GetReplayItemDetail)
		protected.POST("/replay-tasks/:id/rediff", RediffReplayTask)

		// 11. SSE 实时事件流 (GET /api/events)
		protected.GET("/events", HandleEventsSSE)

		// 12. 黄金契约套件自包含快照库 (F-25, ADR-14)
		protected.GET("/golden-suites", ListGoldenSuites)
		protected.GET("/golden-suites/:id", GetGoldenSuite)
		protected.POST("/golden-suites/curate", CurateGoldenSuite)
		protected.PUT("/golden-suites/:id", requireAdmin, UpdateGoldenSuite)
		protected.PUT("/golden-suites/:id/items/:itemId", requireAdmin, UpdateGoldenSuiteItem)
		protected.DELETE("/golden-suites/:id/items/:itemId", requireAdmin, DeleteGoldenSuiteItem)
		protected.DELETE("/golden-suites/:id", requireAdmin, DeleteGoldenSuite)
		protected.POST("/golden-suites/:id/execute", ExecuteGoldenSuite)
	}

	// 13. CI/CD 自动化强阻断质量门禁评估 (F-27, ADR-14, SC-10)
	gates := r.Group("/api/gates")
	gates.Use(CIOrAdminGuard())
	gates.Use(commonAudit.Middleware(config.ServiceName))
	{
		gates.POST("/evaluate", EvaluateGate)
	}
}
