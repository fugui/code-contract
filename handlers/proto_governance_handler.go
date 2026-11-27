package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	commonModels "code-common/backend/models"
	"code-contract/config"
	"code-contract/internal/server/gitclient"
	"code-contract/models"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type repoGovernanceUpdateRequest struct {
	SyncEnabled           *bool                        `json:"sync_enabled"`
	SyncStrategy          *string                      `json:"sync_strategy"`
	ExportTargetSubsystem *string                      `json:"export_target_subsystem"`
	ExportTargetRepoDir   *string                      `json:"export_target_repo_dir"`
	StripPrefixes         []string                     `json:"strip_prefixes"`
	HasStripPrefixes      bool                         `json:"-"`
	PathRewrites          map[string]string            `json:"path_rewrites"`
	CollectGlobs          []string                     `json:"collect_globs"`
	ExcludeGlobs          []string                     `json:"exclude_globs"`
	DisabledRules         []string                     `json:"disabled_rules"`
	RegexRewrites         []gitclient.RegexRewriteRule `json:"regex_rewrites"`
	ImportRewrites        map[string]string            `json:"import_rewrites"`
	ExemptFiles           []string                     `json:"exempt_files"`
	QuarantinedFiles      []string                     `json:"quarantined_files"`
}

func bindRepoGovernancePayload(c *gin.Context, allowEmpty bool) (repoGovernanceUpdateRequest, map[string]json.RawMessage, error) {
	var req repoGovernanceUpdateRequest
	rawBytes, err := io.ReadAll(io.LimitReader(c.Request.Body, 4*1024*1024))
	if err != nil {
		return req, nil, err
	}
	trimmed := bytes.TrimSpace(rawBytes)
	if len(trimmed) == 0 {
		if allowEmpty {
			return req, map[string]json.RawMessage{}, nil
		}
		return req, nil, errors.New("请求体不能为空")
	}
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &rawMap); err != nil {
		return req, nil, err
	}
	if err := json.Unmarshal(trimmed, &req); err != nil {
		return req, nil, err
	}
	_, req.HasStripPrefixes = rawMap["strip_prefixes"]
	return req, rawMap, nil
}

func buildRepoSpecFromPayload(repo models.GitRepo, req repoGovernanceUpdateRequest, rawMap map[string]json.RawMessage) gitclient.RepoGovernanceSpec {
	normCfg := gitclient.ParseRepoNormalizeConfig(repo.RepoNormalizeConfigJSON)
	enabled := repo.SyncEnabled
	if req.SyncEnabled != nil {
		enabled = *req.SyncEnabled
	}
	spec := gitclient.RepoGovernanceSpec{
		PlatformRepoID:        repo.PlatformRepoID,
		SyncEnabled:           &enabled,
		SyncStrategy:          repo.SyncStrategy,
		ExportTargetSubsystem: repo.ExportTargetSubsystem,
		ExportTargetRepoDir:   repo.ExportTargetRepoDir,
		StripPrefixes:         repo.StripPrefixes(),
		PathRewrites:          repo.PathRewrites(),
		CollectGlobs:          repo.CollectGlobs(),
		ExcludeGlobs:          repo.ExcludeGlobs(),
		DisabledRules:         normCfg.DisabledRules,
		RegexRewrites:         normCfg.RegexRewrites,
		ImportRewrites:        normCfg.ImportRewrites,
		ExemptFiles:           normCfg.ExemptFiles,
		QuarantinedFiles:      normCfg.QuarantinedFiles,
	}
	if req.SyncStrategy != nil {
		spec.SyncStrategy = strings.ToUpper(strings.TrimSpace(*req.SyncStrategy))
	}
	if req.ExportTargetSubsystem != nil {
		spec.ExportTargetSubsystem = strings.TrimSpace(*req.ExportTargetSubsystem)
	}
	if req.ExportTargetRepoDir != nil {
		spec.ExportTargetRepoDir = strings.TrimSpace(*req.ExportTargetRepoDir)
	}
	if _, ok := rawMap["strip_prefixes"]; ok {
		spec.StripPrefixes = req.StripPrefixes
	}
	if _, ok := rawMap["path_rewrites"]; ok {
		spec.PathRewrites = req.PathRewrites
	}
	if _, ok := rawMap["collect_globs"]; ok {
		spec.CollectGlobs = req.CollectGlobs
	}
	if _, ok := rawMap["exclude_globs"]; ok {
		spec.ExcludeGlobs = req.ExcludeGlobs
	}
	if _, ok := rawMap["disabled_rules"]; ok {
		spec.DisabledRules = req.DisabledRules
	}
	if _, ok := rawMap["regex_rewrites"]; ok {
		spec.RegexRewrites = req.RegexRewrites
	}
	if _, ok := rawMap["import_rewrites"]; ok {
		spec.ImportRewrites = req.ImportRewrites
	}
	if _, ok := rawMap["exempt_files"]; ok {
		spec.ExemptFiles = req.ExemptFiles
	}
	if _, ok := rawMap["quarantined_files"]; ok {
		spec.QuarantinedFiles = req.QuarantinedFiles
	}
	return spec
}

func validateSingleRepoSpec(repoName string, spec gitclient.RepoGovernanceSpec) error {
	doc := gitclient.DefaultGovernanceConfig()
	doc.Repos[repoName] = spec
	return gitclient.ValidateGovernanceConfig(&doc)
}

func relocateAffectedRepoTrees(protosDir string, repos []models.GitRepo) {
	mgr := gitclient.NewProtoTreeManager(protosDir)
	for _, r := range repos {
		if !r.SyncEnabled {
			mgr.RemoveRepoTree(gitclient.TreeKindClean, r.ID)
			continue
		}
		sub, dir := gitclient.ResolveEffectiveExportTarget(r, nil)
		_, _ = mgr.RelocateRepoCleanTree(r.ID, sub, dir)
	}
}

// GetProtoGovernanceConfigHandler GET /api/proto-governance/config
func GetProtoGovernanceConfigHandler(c *gin.Context) {
	snap, err := gitclient.LoadActiveGovernanceConfig(c.Request.Context(), models.DB)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"doc":           snap.Doc,
		"revision":      snap.Doc.Revision,
		"config_sha256": snap.ConfigSHA256,
		"builtin_rules": gitclient.BuiltinGovernanceRules(),
	})
}

// UpdateProtoGovernanceConfigHandler PUT /api/proto-governance/config
func UpdateProtoGovernanceConfigHandler(c *gin.Context) {
	var doc gitclient.GovernanceConfigDoc
	if err := c.ShouldBindJSON(&doc); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "治理配置 JSON 格式非法: " + err.Error()})
		return
	}
	beforeSnap, _ := gitclient.LoadActiveGovernanceConfig(c.Request.Context(), models.DB)
	_, opName := currentOperatorName(c)

	snap, affectedRepos, cols, err := gitclient.SaveGovernanceConfig(c.Request.Context(), models.DB, doc, opName)
	if err != nil {
		if errors.Is(err, gitclient.ErrExportTargetConflict) {
			c.JSON(http.StatusConflict, gin.H{
				"err_code":   "EXPORT_TARGET_CONFLICT",
				"error":      err.Error(),
				"collisions": cols,
			})
			return
		}
		if errors.Is(err, gitclient.ErrGovernanceValidation) || errors.Is(err, gitclient.ErrIntraRepoPathConflict) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	relocateAffectedRepoTrees(config.AppConfig.Storage.GetProtosDir(), affectedRepos)

	var beforeDoc any
	if beforeSnap != nil {
		beforeDoc = beforeSnap.Doc
	}
	recordAudit(c, "contract_git:update_governance_config", commonModels.AuditLevelP1,
		fmt.Sprintf("管理员 [%s] 更新全局契约导出与清洗治理配置 (revision=%d)", opName, snap.Doc.Revision),
		"proto_governance_config", uint64(snap.Doc.Revision), snap.ConfigSHA256, beforeDoc, snap.Doc)

	c.JSON(http.StatusOK, gin.H{
		"doc":            snap.Doc,
		"revision":       snap.Doc.Revision,
		"config_sha256":  snap.ConfigSHA256,
		"affected_repos": len(affectedRepos),
	})
}

type protoGovernanceDryRunRequest struct {
	Config  *gitclient.GovernanceConfigDoc `json:"config"`
	RepoIDs []uint64                       `json:"repo_ids"`
}

// DryRunProtoGovernanceHandler POST /api/proto-governance/dry-run
func DryRunProtoGovernanceHandler(c *gin.Context) {
	rawBytes, err := io.ReadAll(io.LimitReader(c.Request.Body, 4*1024*1024))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var doc gitclient.GovernanceConfigDoc
	var repoIDs []uint64
	if len(bytes.TrimSpace(rawBytes)) > 0 {
		var wrapper protoGovernanceDryRunRequest
		if uErr := json.Unmarshal(rawBytes, &wrapper); uErr == nil && wrapper.Config != nil {
			doc = *wrapper.Config
			repoIDs = wrapper.RepoIDs
		} else if uErr := json.Unmarshal(rawBytes, &doc); uErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "治理配置 JSON 格式非法: " + uErr.Error()})
			return
		} else {
			repoIDs = wrapper.RepoIDs
		}
	} else {
		snap, lErr := gitclient.LoadActiveGovernanceConfig(c.Request.Context(), models.DB)
		if lErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": lErr.Error()})
			return
		}
		doc = snap.Doc
	}

	out := gitclient.GlobalGovernanceDryRunResult{
		SchemaValid:   true,
		AffectedRepos: []gitclient.RepoGovernanceDryRunResult{},
	}
	if vErr := gitclient.ValidateGovernanceConfig(&doc); vErr != nil {
		out.SchemaValid = false
		out.ValidationError = vErr.Error()
		c.JSON(http.StatusOK, out)
		return
	}

	var repos []models.GitRepo
	q := models.DB.WithContext(c.Request.Context()).Order("id ASC")
	if len(repoIDs) > 0 {
		q = q.Where("id IN ?", repoIDs)
	}
	if err := q.Find(&repos).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	overrides := make(map[uint64]*gitclient.RepoGovernanceSpec)
	var targetRepos []models.GitRepo
	for _, r := range repos {
		if spec, ok := doc.Repos[r.Name]; ok {
			sp := spec
			overrides[r.ID] = &sp
			targetRepos = append(targetRepos, r)
		} else {
			matchedByPlat := false
			for _, rs := range doc.Repos {
				if rs.PlatformRepoID > 0 && rs.PlatformRepoID == r.PlatformRepoID {
					sp := rs
					overrides[r.ID] = &sp
					targetRepos = append(targetRepos, r)
					matchedByPlat = true
					break
				}
			}
			if !matchedByPlat && len(repoIDs) > 0 {
				targetRepos = append(targetRepos, r)
			}
		}
	}
	if len(targetRepos) == 0 && len(repos) > 0 {
		limit := len(repos)
		if limit > 20 {
			limit = 20
		}
		targetRepos = repos[:limit]
	}

	if cols, _ := gitclient.CheckGlobalExportTargetCollisions(c.Request.Context(), models.DB, overrides); len(cols) > 0 {
		out.GlobalCollisions = cols
	}
	for _, r := range targetRepos {
		ov := overrides[r.ID]
		dr, drErr := gitclient.DryRunRepoGovernance(c.Request.Context(), models.DB, r, ov, &doc)
		if drErr == nil && dr != nil {
			out.AffectedRepos = append(out.AffectedRepos, *dr)
		}
	}
	c.JSON(http.StatusOK, out)
}

type applyAndRenormalizeRequest struct {
	Config         *gitclient.GovernanceConfigDoc `json:"config"`
	RepoIDs        []uint64                       `json:"repo_ids"`
	ForceRecompile bool                           `json:"force_recompile"`
}

// ApplyAndRenormalizeGovernanceHandler POST /api/proto-governance/apply-and-renormalize
func ApplyAndRenormalizeGovernanceHandler(c *gin.Context) {
	var req applyAndRenormalizeRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法: " + err.Error()})
		return
	}
	uid, opName := currentOperatorName(c)

	var (
		snap          *gitclient.GovernanceConfigSnapshot
		affectedRepos []models.GitRepo
	)
	if req.Config != nil {
		s, aff, cols, err := gitclient.SaveGovernanceConfig(c.Request.Context(), models.DB, *req.Config, opName)
		if err != nil {
			if errors.Is(err, gitclient.ErrExportTargetConflict) {
				c.JSON(http.StatusConflict, gin.H{
					"err_code":   "EXPORT_TARGET_CONFLICT",
					"error":      err.Error(),
					"collisions": cols,
				})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		snap = s
		affectedRepos = aff
		relocateAffectedRepoTrees(config.AppConfig.Storage.GetProtosDir(), affectedRepos)
	} else {
		var err error
		snap, err = gitclient.LoadActiveGovernanceConfig(c.Request.Context(), models.DB)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	targetIDs := append([]uint64(nil), req.RepoIDs...)
	if len(targetIDs) == 0 && len(affectedRepos) > 0 {
		for _, r := range affectedRepos {
			if r.SyncEnabled {
				targetIDs = append(targetIDs, r.ID)
			}
		}
	}
	if len(targetIDs) == 0 {
		_ = models.DB.WithContext(c.Request.Context()).Model(&models.GitRepo{}).
			Where("sync_enabled = true AND proto_status = ?", models.GitRepoProtoStatusHasProto).
			Order("id ASC").
			Pluck("id", &targetIDs).Error
	}

	decReg, _ := GetDecoderRegistry()
	broker := GetGlobalSSEBroker()
	protosDir := config.AppConfig.Storage.GetProtosDir()

	results := make([]*gitclient.PipelineRunResult, 0, len(targetIDs))
	errs := make(map[string]string)
	for _, rid := range targetIDs {
		res, rErr := gitclient.ReNormalizeRepo(c.Request.Context(), models.DB, decReg, MakeSSEEventBroadcaster(broker), rid, gitclient.PipelineOptions{
			OperatorID:         uid,
			ForceRecompile:     req.ForceRecompile,
			ForceRenormalize:   true,
			ProtosDir:          protosDir,
			TriggerBlastRadius: true,
		})
		if rErr != nil {
			errs[strconv.FormatUint(rid, 10)] = rErr.Error()
		} else if res != nil {
			results = append(results, res)
		}
	}

	recordAudit(c, "contract_git:apply_governance_renormalize", commonModels.AuditLevelP1,
		fmt.Sprintf("管理员 [%s] 应用治理配置 (rev=%d) 并免网络重清洗 %d 个仓库", opName, snap.Doc.Revision, len(targetIDs)),
		"proto_governance_config", uint64(snap.Doc.Revision), snap.ConfigSHA256, nil, gin.H{
			"repo_count": len(targetIDs),
			"succeeded":  len(results),
			"failed":     len(errs),
		})

	c.JSON(http.StatusOK, gin.H{
		"revision":        snap.Doc.Revision,
		"config_sha256":   snap.ConfigSHA256,
		"processed_count": len(targetIDs),
		"succeeded_count": len(results),
		"failed_count":    len(errs),
		"results":         results,
		"errors":          errs,
	})
}

// UpdateGitRepoGovernanceHandler PUT /api/git-repos/:id/governance
func UpdateGitRepoGovernanceHandler(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法的仓库 ID"})
		return
	}

	repo, bundles, err := loadRepoWithBundles(models.DB, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": gitclient.ErrGitRepoNotFound.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	beforeDTO := toGitRepoResponseDTO(*repo, bundles)

	req, rawMap, err := bindRepoGovernancePayload(c, false)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "治理载荷格式非法: " + err.Error()})
		return
	}
	spec := buildRepoSpecFromPayload(*repo, req, rawMap)
	if vErr := validateSingleRepoSpec(repo.Name, spec); vErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": vErr.Error()})
		return
	}

	overrides := map[uint64]*gitclient.RepoGovernanceSpec{repo.ID: &spec}
	if cols, cErr := gitclient.CheckGlobalExportTargetCollisions(c.Request.Context(), models.DB, overrides); cErr != nil {
		c.JSON(http.StatusConflict, gin.H{
			"err_code":   "EXPORT_TARGET_CONFLICT",
			"error":      cErr.Error(),
			"collisions": cols,
		})
		return
	}

	var existingPaths []string
	_ = models.DB.WithContext(c.Request.Context()).Model(&models.ProtoFile{}).
		Where("git_repo_id = ? AND is_deleted = false", repo.ID).
		Pluck("repo_file_path", &existingPaths).Error
	if len(existingPaths) > 0 {
		if _, pCols, pErr := gitclient.MapAndValidateIntraRepoPaths(existingPaths, spec.StripPrefixes, spec.PathRewrites); pErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"err_code":   "INTRA_REPO_PATH_CONFLICT",
				"error":      pErr.Error(),
				"collisions": pCols,
			})
			return
		}
	}

	if spec.SyncEnabled != nil {
		repo.SyncEnabled = *spec.SyncEnabled
		if !repo.SyncEnabled {
			repo.ProtoStatus = models.GitRepoProtoStatusExcluded
		} else if repo.ProtoStatus == models.GitRepoProtoStatusExcluded {
			repo.ProtoStatus = models.GitRepoProtoStatusPending
		}
	}
	if spec.SyncStrategy != "" {
		repo.SyncStrategy = spec.SyncStrategy
	}
	repo.ExportTargetSubsystem = spec.ExportTargetSubsystem
	repo.ExportTargetRepoDir = spec.ExportTargetRepoDir
	if b, err := json.Marshal(spec.StripPrefixes); err == nil {
		repo.StripPrefixesJSON = datatypes.JSON(b)
	}
	if spec.PathRewrites != nil {
		if b, err := json.Marshal(spec.PathRewrites); err == nil {
			repo.PathRewritesJSON = datatypes.JSON(b)
		}
	}
	if _, ok := rawMap["collect_globs"]; ok {
		if b, err := json.Marshal(req.CollectGlobs); err == nil {
			repo.CollectGlobsJSON = datatypes.JSON(b)
		}
	}
	if _, ok := rawMap["exclude_globs"]; ok {
		if b, err := json.Marshal(req.ExcludeGlobs); err == nil {
			repo.ExcludeGlobsJSON = datatypes.JSON(b)
		}
	}
	normCfg := gitclient.RepoNormalizeConfig{
		DisabledRules:    spec.DisabledRules,
		RegexRewrites:    spec.RegexRewrites,
		ImportRewrites:   spec.ImportRewrites,
		ExemptFiles:      spec.ExemptFiles,
		QuarantinedFiles: spec.QuarantinedFiles,
	}
	if b, err := json.Marshal(normCfg); err == nil {
		repo.RepoNormalizeConfigJSON = datatypes.JSON(b)
	}

	if err := models.DB.WithContext(c.Request.Context()).Save(repo).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	relocateAffectedRepoTrees(config.AppConfig.Storage.GetProtosDir(), []models.GitRepo{*repo})

	afterDTO := toGitRepoResponseDTO(*repo, bundles)
	if repo.PlatformRepoID > 0 {
		var p models.Repository
		if err := models.DB.Preload("Department").Preload("Owner").First(&p, repo.PlatformRepoID).Error; err == nil {
			afterDTO.ServiceGroup = p.ServiceGroup
			afterDTO.DepartmentID = p.DepartmentID
			afterDTO.DepartmentName = p.Department.Name
			afterDTO.OwnerID = p.OwnerID
			afterDTO.OwnerName = p.Owner.Username
			afterDTO.IsPlatformActive = p.IsActive
		}
	}

	recordAudit(c, "contract_git:update_repo_governance", commonModels.AuditLevelP2,
		fmt.Sprintf("更新仓库 [%s] 导出目录与清洗治理配置", repo.Name),
		"git_repo", repo.ID, repo.Name, beforeDTO, afterDTO)

	c.JSON(http.StatusOK, afterDTO)
}

// DryRunGitRepoGovernanceHandler POST /api/git-repos/:id/governance/dry-run
func DryRunGitRepoGovernanceHandler(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法的仓库 ID"})
		return
	}

	var repo models.GitRepo
	if err := models.DB.WithContext(c.Request.Context()).First(&repo, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": gitclient.ErrGitRepoNotFound.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	req, rawMap, err := bindRepoGovernancePayload(c, true)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "试算参数格式非法: " + err.Error()})
		return
	}

	var specPtr *gitclient.RepoGovernanceSpec
	if len(rawMap) > 0 {
		spec := buildRepoSpecFromPayload(repo, req, rawMap)
		if vErr := validateSingleRepoSpec(repo.Name, spec); vErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": vErr.Error()})
			return
		}
		specPtr = &spec
	}

	res, err := gitclient.DryRunRepoGovernance(c.Request.Context(), models.DB, repo, specPtr, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}
