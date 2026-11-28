package diff

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"code-contract/internal/server/decoder"
	"code-contract/models"
	"code-contract/pkg/agentproto"
	"code-contract/pkg/crosrpc"
	"gorm.io/datatypes"
)

type evaluatedReplayItem struct {
	ResultStatus    string
	FailedLayer     int
	FieldDiffs      []FieldDiff
	ActualStatus    uint64
	ActualPayload   datatypes.JSON
	RawActualPacket []byte
	MockTrace       []agentproto.MockTraceEvent
	LatencyUs       int64
	ErrorMessage    string
}

// extractReplayItemDetails 优先读取结构化 Items 字段，并向后兼容反序列化 Details JSON 数组
func extractReplayItemDetails(result *agentproto.ReplayTaskResultResp) map[uint64]agentproto.ReplayItemDetail {
	detailMap := make(map[uint64]agentproto.ReplayItemDetail)
	if result == nil {
		return detailMap
	}
	if len(result.Items) > 0 {
		for _, d := range result.Items {
			detailMap[d.RecordID] = d
		}
		return detailMap
	}
	if strings.TrimSpace(result.Details) != "" && strings.HasPrefix(strings.TrimSpace(result.Details), "[") {
		var details []agentproto.ReplayItemDetail
		if err := json.Unmarshal([]byte(result.Details), &details); err == nil {
			for _, d := range details {
				detailMap[d.RecordID] = d
			}
		}
	}
	return detailMap
}

func isOnewayRequestFrame(rawReq []byte) bool {
	if len(rawReq) > 24+136 {
		return rawReq[24+136] == crosrpc.ReqTypeOneway
	}
	return false
}

func decodeActualPayloadJSON(dec *decoder.Registry, serviceName, operation string, rawRsp []byte) datatypes.JSON {
	if dec == nil || len(rawRsp) == 0 {
		return nil
	}
	frames, err := crosrpc.SplitFrames(rawRsp)
	if err == nil && len(frames) > 1 {
		var merged []byte
		for _, fr := range frames {
			if parsed, pErr := crosrpc.ParsePacket(fr); pErr == nil && len(fr) > parsed.PayloadOffset {
				merged = append(merged, fr[parsed.PayloadOffset:]...)
			}
		}
		if len(merged) > 0 && serviceName != "" && operation != "" {
			res := dec.DecodePayloadToJSON(serviceName, operation, crosrpc.MsgTypeRsp, merged)
			if len(res.JSONBytes) > 0 {
				return datatypes.JSON(res.JSONBytes)
			}
		}
	}
	decRes, dErr := dec.DecodeCrosFrame(rawRsp, false)
	if dErr == nil && decRes != nil && len(decRes.Decode.JSONBytes) > 0 {
		return datatypes.JSON(decRes.Decode.JSONBytes)
	}
	if serviceName != "" && operation != "" {
		if parsed, pErr := crosrpc.ParsePacket(rawRsp); pErr == nil && len(rawRsp) > parsed.PayloadOffset {
			res := dec.DecodePayloadToJSON(serviceName, operation, crosrpc.MsgTypeRsp, rawRsp[parsed.PayloadOffset:])
			if len(res.JSONBytes) > 0 {
				return datatypes.JSON(res.JSONBytes)
			}
		}
	}
	return nil
}

// diffHeadersAndPayloads 支持单帧与多段 RSP_SEG 拼接缓冲的双层回归比对
func diffHeadersAndPayloads(
	dec *decoder.Registry,
	serviceName, operation string,
	expectedRsp, actualRsp []byte,
	ignorePaths []string,
) (diffResult *DiffResult, fieldDiffs []FieldDiff, failedLayer int, errMsg string) {
	expHeadFrame := expectedRsp
	actHeadFrame := actualRsp
	var expMergedPayload, actMergedPayload []byte

	if expFrames, err := crosrpc.SplitFrames(expectedRsp); err == nil && len(expFrames) > 1 {
		expHeadFrame = expFrames[len(expFrames)-1]
		for _, fr := range expFrames {
			if parsed, pErr := crosrpc.ParsePacket(fr); pErr == nil && len(fr) > parsed.PayloadOffset {
				expMergedPayload = append(expMergedPayload, fr[parsed.PayloadOffset:]...)
			}
		}
	}
	if actFrames, err := crosrpc.SplitFrames(actualRsp); err == nil && len(actFrames) > 1 {
		actHeadFrame = actFrames[len(actFrames)-1]
		for _, fr := range actFrames {
			if parsed, pErr := crosrpc.ParsePacket(fr); pErr == nil && len(fr) > parsed.PayloadOffset {
				actMergedPayload = append(actMergedPayload, fr[parsed.PayloadOffset:]...)
			}
		}
	}

	var payloadIdentical bool
	if len(expMergedPayload) > 0 || len(actMergedPayload) > 0 {
		// 多段帧先比对终结帧头部一致性，再比对合并后的完整 Payload
		dr, _, l1Err := DiffLayer1Header(expHeadFrame, actHeadFrame)
		diffResult = dr
		if l1Err != nil || (dr != nil && dr.Status == models.ReplayResultFailed && len(dr.FieldDiffs) > 0) {
			if dr != nil {
				fieldDiffs = dr.FieldDiffs
				errMsg = dr.FailureReason
			}
			return diffResult, fieldDiffs, 1, errMsg
		}
		payloadIdentical = string(expMergedPayload) == string(actMergedPayload)
		if !payloadIdentical && dec != nil {
			op := dec.LookupOperation(serviceName, operation)
			fd, fErr := DiffLayer2ProtoSemantic(expMergedPayload, actMergedPayload, op, ignorePaths)
			if len(fd) > 0 || fErr != nil {
				fieldDiffs = append(fieldDiffs, fd...)
				if fErr != nil {
					errMsg = fErr.Error()
				}
				return diffResult, fieldDiffs, 2, errMsg
			}
		}
		return diffResult, nil, 0, ""
	}

	dr, identical, dErr := DiffLayer1Header(expectedRsp, actualRsp)
	diffResult = dr
	payloadIdentical = identical
	if dErr != nil {
		return diffResult, nil, 1, dErr.Error()
	}
	if diffResult != nil && diffResult.Status == models.ReplayResultFailed {
		return diffResult, diffResult.FieldDiffs, 1, diffResult.FailureReason
	}
	if !payloadIdentical && dec != nil {
		op := dec.LookupOperation(serviceName, operation)
		parsedExp, errExp := crosrpc.ParsePacket(expectedRsp)
		parsedAct, errAct := crosrpc.ParsePacket(actualRsp)
		if errExp != nil || errAct != nil || parsedExp == nil || parsedAct == nil {
			return diffResult, []FieldDiff{{FieldPath: "header.frame", DiffKind: "MODIFIED", Expected: "valid crosrpc", Actual: fmt.Sprintf("expErr=%v,actErr=%v", errExp, errAct)}}, 1, "CROS 帧头解析失败"
		}
		expPl := expectedRsp[parsedExp.PayloadOffset:]
		actPl := actualRsp[parsedAct.PayloadOffset:]
		fd, fErr := DiffLayer2ProtoSemantic(expPl, actPl, op, ignorePaths)
		if len(fd) > 0 || fErr != nil {
			fieldDiffs = append(fieldDiffs, fd...)
			if fErr != nil {
				errMsg = fErr.Error()
			}
			return diffResult, fieldDiffs, 2, errMsg
		}
	}
	return diffResult, nil, 0, ""
}

// evaluateReplayItem 共享重放单条Fail-Closed回收、解码与双层断言评估管线 (ADR-31 D2)
func evaluateReplayItem(
	dec *decoder.Registry,
	serviceName, operation, exchangeType string,
	rawReq, expectedRsp []byte,
	detail *agentproto.ReplayItemDetail,
	hasDetail bool,
	resultFallbackStatus string,
	ignorePaths []string,
) evaluatedReplayItem {
	out := evaluatedReplayItem{
		ResultStatus: models.ReplayResultPass,
		LatencyUs:    1,
	}
	if detail != nil {
		if detail.LatencyUs > 0 {
			out.LatencyUs = detail.LatencyUs
		}
		out.MockTrace = detail.MockTrace
		out.ErrorMessage = detail.ErrorMessage
	}

	if hasDetail && detail != nil && len(detail.ActualRspHex) > 0 {
		if hBytes, hErr := hex.DecodeString(detail.ActualRspHex); hErr == nil && len(hBytes) > 0 {
			out.RawActualPacket = hBytes
			out.ActualPayload = decodeActualPayloadJSON(dec, serviceName, operation, hBytes)
		} else if hErr != nil {
			out.ErrorMessage = fmt.Sprintf("actual_rsp_hex 十六进制解码失败: %v", hErr)
		}
	}

	isOneway := exchangeType == models.ExchangeOneway || isOnewayRequestFrame(rawReq)
	if isOneway {
		if hasDetail && detail != nil && (detail.Status == models.ReplayResultMockMiss || detail.Status == models.ReplayResultTimeout || detail.Status == models.ReplayResultError) {
			out.ResultStatus = detail.Status
			out.FailedLayer = 1
		}
		return out
	}

	// Fail-Closed：无明细或明细显式失败
	if !hasDetail || detail == nil {
		if resultFallbackStatus == models.ReplayResultMockMiss || resultFallbackStatus == models.ReplayResultTimeout {
			out.ResultStatus = resultFallbackStatus
		} else {
			out.ResultStatus = models.ReplayResultError
		}
		out.FailedLayer = 1
		if out.ErrorMessage == "" {
			out.ErrorMessage = "Agent 未返回该条请求的重放执行明细 (Fail-Closed)"
		}
		return out
	}

	if detail.Status == models.ReplayResultMockMiss || detail.Status == models.ReplayResultTimeout || detail.Status == models.ReplayResultError {
		out.ResultStatus = detail.Status
		out.FailedLayer = 1
		if out.ErrorMessage == "" {
			out.ErrorMessage = fmt.Sprintf("Agent 上报重放异常状态: %s", detail.Status)
		}
		return out
	}

	if len(out.RawActualPacket) == 0 {
		out.ResultStatus = models.ReplayResultError
		out.FailedLayer = 1
		if out.ErrorMessage == "" {
			out.ErrorMessage = "未捕获到实际应答报文 (actual_rsp_hex 为空，已禁止自比较伪绿)"
		}
		return out
	}

	diffRes, diffs, layer, errMsg := diffHeadersAndPayloads(dec, serviceName, operation, expectedRsp, out.RawActualPacket, ignorePaths)
	if diffRes != nil {
		out.ActualStatus = diffRes.ActualStatus
	}
	if layer > 0 {
		out.ResultStatus = models.ReplayResultFailed
		out.FailedLayer = layer
		out.FieldDiffs = diffs
		if out.ErrorMessage == "" {
			out.ErrorMessage = errMsg
		}
	}
	return out
}

func appendUniqueIgnorePaths(base []string, extra []string) []string {
	seen := make(map[string]bool, len(base)+len(extra))
	var out []string
	for _, p := range base {
		p = strings.TrimSpace(p)
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, p := range extra {
		p = strings.TrimSpace(p)
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
