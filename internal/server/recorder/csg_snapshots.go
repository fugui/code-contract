package recorder

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"code-contract/models"
	"code-contract/pkg/agentproto"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

var csgSessionMergeMu sync.Mutex

func (o *Orchestrator) collectInitialCSGSnapshots(ctx context.Context, agentIDs []uint64, _ uint, _ bool) []byte {
	var collected []agentproto.CSGSnapshotRecord
	seen := make(map[string]struct{})
	nowStr := time.Now().UTC().Format(time.RFC3339)

	if o.db != nil && len(agentIDs) > 0 {
		var agents []models.Agent
		if err := o.db.WithContext(ctx).Where("id IN ?", agentIDs).Find(&agents).Error; err == nil {
			baseClient := o.httpClient
			snapshotClient := &http.Client{
				Timeout: 15 * time.Second,
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					return http.ErrUseLastResponse
				},
			}
			if baseClient != nil {
				snapshotClient.Transport = baseClient.Transport
			}
			subCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()

			for _, ag := range agents {
				if ag.Status != models.AgentStatusOnline || ag.Endpoint == "" {
					continue
				}
				req, err := http.NewRequestWithContext(subCtx, http.MethodGet, ag.Endpoint+"/agent/v1/csg/topology", nil)
				if err != nil {
					continue
				}
				if ag.ClaimCode != "" {
					req.Header.Set(agentproto.HeaderClaimCode, ag.ClaimCode)
				}
				resp, err := snapshotClient.Do(req)
				if err != nil || resp == nil {
					log.Printf("[Orchestrator] 采集 Agent#%d (%s) 初始 CSG 快照失败: %v", ag.ID, ag.Endpoint, err)
					continue
				}
				if resp.StatusCode == http.StatusOK {
					var rep agentproto.CSGTopologyReport
					if decErr := json.NewDecoder(resp.Body).Decode(&rep); decErr == nil {
						if len(rep.Services) == 0 && len(rep.Errors) > 0 {
							log.Printf("[Orchestrator] Agent#%d 初始 CSG 快照为空 (mode=%s): %v", ag.ID, rep.DiscoveryMode, rep.Errors)
						}
						for _, svc := range rep.Services {
							key := svc.ServiceName + "|" + svc.Addr
							if _, ok := seen[key]; ok {
								continue
							}
							seen[key] = struct{}{}
							collected = append(collected, agentproto.CSGSnapshotRecord{
								ServiceName: svc.ServiceName,
								Addr:        svc.Addr,
								Transport:   svc.Transport,
								HostIP:      svc.HostIP,
								Port:        svc.Port,
								Source:      "START_SESSION",
								ObservedAt:  nowStr,
							})
						}
					}
				}
				_ = resp.Body.Close()
			}
		}
	}

	if len(collected) == 0 {
		return []byte("[]")
	}
	raw, err := json.Marshal(collected)
	if err != nil {
		return []byte("[]")
	}
	return raw
}

func (o *Orchestrator) handleCSGObservationPayload(sessionID uint64, payload []byte) {
	if o == nil || o.db == nil || len(payload) == 0 {
		return
	}
	var obs agentproto.CSGObservation
	if err := json.Unmarshal(payload, &obs); err != nil {
		log.Printf("[Orchestrator] 解析 0x08 CSGObservation 载荷失败: %v", err)
		return
	}
	if len(obs.Snapshots) == 0 {
		return
	}

	csgSessionMergeMu.Lock()
	defer csgSessionMergeMu.Unlock()

	_ = o.db.Transaction(func(tx *gorm.DB) error {
		var sess models.RecordingSession
		if err := tx.Select("id, csg_snapshots").First(&sess, sessionID).Error; err != nil {
			return err
		}
		var existing []agentproto.CSGSnapshotRecord
		if len(sess.CSGSnapshots) > 0 {
			_ = json.Unmarshal(sess.CSGSnapshots, &existing)
		}
		merged := mergeCSGSnapshotRecords(existing, obs.Snapshots)
		encoded, err := json.Marshal(merged)
		if err != nil {
			return err
		}
		return tx.Model(&models.RecordingSession{}).
			Where("id = ?", sessionID).
			Update("csg_snapshots", datatypes.JSON(encoded)).Error
	})
}

func mergeCSGSnapshotRecords(existing, incoming []agentproto.CSGSnapshotRecord) []agentproto.CSGSnapshotRecord {
	idxByKey := make(map[string]int, len(existing)+len(incoming))
	out := make([]agentproto.CSGSnapshotRecord, 0, len(existing)+len(incoming))
	for _, item := range existing {
		key := item.ServiceName + "|" + item.Addr
		idxByKey[key] = len(out)
		out = append(out, item)
	}
	for _, item := range incoming {
		if item.ServiceName == "" && item.Addr == "" {
			continue
		}
		key := item.ServiceName + "|" + item.Addr
		if idx, exists := idxByKey[key]; exists {
			if item.ObservedAt != "" {
				out[idx].ObservedAt = item.ObservedAt
			}
			continue
		}
		idxByKey[key] = len(out)
		out = append(out, item)
	}
	return out
}
