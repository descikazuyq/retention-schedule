package retention

import (
	"fmt"
)

// validateBatchInputs 校验一批销毁/核对请求共有的输入：
// 申请编号、处理日期、非空且去空白后无重复的档案名单。
// 返回去首尾空白后的申请编号、按提交顺序规范化的名单及名单集合。
// 任一输入无效即返回错误，调用方不得据此产生部分结果。
func validateBatchInputs(applicationID string, processedOn Date, rawIDs []string) (string, []string, map[string]struct{}, error) {
	appID, err := requireText(applicationID, "申请编号")
	if err != nil {
		return "", nil, nil, err
	}
	if err := requireDate(processedOn, "处理日期"); err != nil {
		return "", nil, nil, err
	}
	if len(rawIDs) == 0 {
		return "", nil, nil, fmt.Errorf("retention: 申请 %s 的销毁名单为空: %w",
			appID, ErrEmptyDestructionList)
	}

	// 规范化名单并检查名单内重复（输入校验先于任何业务判断）。
	// 同一编号仅因首尾空白不同也视为重复。
	ids := make([]string, 0, len(rawIDs))
	for _, raw := range rawIDs {
		id, err := requireText(raw, "档案编号")
		if err != nil {
			return "", nil, nil, err
		}
		ids = append(ids, id)
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			return "", nil, nil, fmt.Errorf("retention: 申请 %s 的销毁名单中档案 %s 重复: %w",
				appID, id, ErrDuplicateSelection)
		}
		seen[id] = struct{}{}
	}
	return appID, ids, seen, nil
}

// evaluateArchive 按当前保存状态核对一份档案在给定处理日期下的全部销毁阻碍，
// 是只读核对（Check）与正式销毁（Destroy）共用的同一份业务规则：
// 到期以当前生效的保管截止日（含修订后的日期）为准，处理日期不早于截止日即到期；
// 任一未解除冻结都是阻碍，已解除记录只保留在历史中。
//
// 阻碍按固定顺序列出：不存在、已销毁、未到期，然后是每条未解除冻结各一条
// （沿用冻结登记顺序）。已销毁的档案只带已销毁阻碍，并附所属清册的申请编号
// 和处理日期；不存在的编号只带不存在阻碍。返回结果同时携带登记内容，
// 供核对报告展示，也供正式销毁在失败时定位档案。
func evaluateArchive(data *storeData, id string, processedOn Date) ArchiveCheckResult {
	r := ArchiveCheckResult{ID: id, Obstructions: []CheckObstruction{}}
	ar, ok := data.Archives[id]
	if !ok {
		r.Obstructions = append(r.Obstructions, CheckObstruction{Kind: ObstructionMissing})
		return r
	}
	r.Exists = true
	r.Category = ar.Category
	r.Start = ar.Start
	r.End = ar.End
	r.Destroyed = ar.Destroyed

	if ar.Destroyed {
		// 已销毁档案给出所属清册的申请编号和处理日期。
		obs := CheckObstruction{Kind: ObstructionDestroyed}
		if rec, ok := data.Manifests[ar.ManifestID]; ok {
			obs.ManifestApplicationID = rec.ApplicationID
			// 通过校验的已关闭清册必然带有有效处理日期。
			if rec.ProcessedOn != nil {
				obs.ProcessedOn = *rec.ProcessedOn
			}
		} else {
			obs.ManifestApplicationID = ar.ManifestID
		}
		r.Obstructions = append(r.Obstructions, obs)
		return r
	}

	// 未到期与未解除冻结可能同时成立，两类问题都要列出。
	if processedOn.Before(ar.End) {
		r.Obstructions = append(r.Obstructions, CheckObstruction{Kind: ObstructionNotExpired})
	}
	// 每条未解除冻结各成一条阻碍，顺序沿用冻结历史（登记顺序）。
	for _, f := range ar.Freezes {
		if f.Released {
			continue
		}
		obs := CheckObstruction{
			Kind: ObstructionActiveFreeze,
			Freeze: ActiveFreezeInfo{
				FreezeID: f.ID,
				Reason:   f.Reason,
			},
		}
		// 通过 load 校验的冻结必然带有有效冻结日期；防御性保留零值兜底。
		if f.FrozenOn != nil {
			obs.Freeze.FrozenOn = *f.FrozenOn
		}
		r.Obstructions = append(r.Obstructions, obs)
	}
	return r
}

// firstObstructionError 把共用核对结果中的第一条阻碍转换为正式销毁的失败错误。
// 阻碍顺序固定（不存在、已销毁、未到期、未解除冻结），因此同一档案同时存在
// 多个问题时，错误类别与定位信息与既有判断先后一致。
func firstObstructionError(r ArchiveCheckResult, processedOn Date) error {
	switch obs := r.Obstructions[0]; obs.Kind {
	case ObstructionMissing:
		return fmt.Errorf("retention: 档案 %s 不存在: %w", r.ID, ErrNotFound)
	case ObstructionDestroyed:
		return fmt.Errorf("retention: 档案 %s 已在清册 %s 中，不能再次销毁: %w",
			r.ID, obs.ManifestApplicationID, ErrArchiveAlreadyOnManifest)
	case ObstructionNotExpired:
		return fmt.Errorf("retention: 档案 %s 尚未到期（截止日 %s，处理日期 %s）: %w",
			r.ID, r.End, processedOn, ErrNotExpired)
	case ObstructionActiveFreeze:
		return fmt.Errorf("retention: 档案 %s 有未解除的冻结 %s: %w",
			r.ID, obs.Freeze.FreezeID, ErrActiveFreeze)
	}
	return nil
}

// Check 在正式办理销毁前做一次只读核对。
//
// 调用者提交申请编号、处理日期和档案编号名单，得到整批申请能否办理的报告，
// 一次看清所有阻碍。核对不销毁档案、不生成或改写清册，也不占用申请编号；
// 核对后（无论报告结论如何，甚至读取本地记录失败）仍可沿用同一编号继续
// 核对或正式提交。
//
// 报告区分四种结论：
//   - CheckReady 可以办理：申请编号尚未成功使用，名单中每份档案均无阻碍；
//   - CheckBlocked 存在阻碍：申请编号尚未成功使用，至少一份档案存在阻碍，
//     Results 按提交顺序列出每份档案的核对结果和全部适用阻碍；
//   - CheckReplayable 可以取回原清册：申请编号已成功使用，且日期与档案集合
//     （顺序无关）与原申请相同，Manifest 返回原清册，不因档案已销毁而判受阻；
//   - CheckConflict 申请编号冲突：沿用该编号但改变了日期或集合，Manifest 附原清册。
//
// 申请编号已被本保管库内任意一条成功的期限修订占用时（与本次选中的档案无关，
// 该档案后来再次修订或已销毁都不释放编号），整次核对明确失败，返回
// ErrRevisionConflict，报告为空——不列出逐份档案结果，也不附清册。
// 只有成功修订才占用编号；失败过的修订提交不影响核对。
//
// 到期与冻结判断与 Destroy 共用同一份实现（evaluateArchive）：处理日期达到
// 截止日当天即到期，任一未解除冻结都会阻止销毁，已解除记录不作为阻碍。
//
// 申请编号或任一档案编号为空白、日期缺失或不是真实的 YYYY-MM-DD、
// 名单为空或含重复编号时，整次核对明确失败，不返回部分报告；
// 读取本地记录失败同样明确失败，绝不会用旧报告充当本次结果。
//
// 报告中的档案结果与清册来自同一次已保存状态读取；报告只说明核对当时的
// 情况，之后状态若变化，正式提交仍按最新状态判断。返回值是复制生成的
// 只读视图，调用者修改报告或其中清册不影响已保存的历史。
func (s *Store) Check(req CheckRequest) (CheckReport, error) {
	appID, ids, seen, err := validateBatchInputs(req.ApplicationID, req.ProcessedOn, req.ArchiveIDs)
	if err != nil {
		return CheckReport{}, err
	}

	var report CheckReport
	err = s.view(func(data *storeData) error {
		report = CheckReport{
			ApplicationID: appID,
			ProcessedOn:   req.ProcessedOn,
			Results:       make([]ArchiveCheckResult, 0, len(ids)),
		}

		// 申请编号已成功使用：只判断能否取回原清册或编号冲突，两者都附原清册。
		// 原清册取自当前这一次状态读取，与下面逐份核对看到的记录同源。
		if existing, ok := data.Manifests[appID]; ok {
			m := manifestFromRecord(existing)
			report.Manifest = &m
			if sameApplication(existing, req.ProcessedOn, seen) {
				report.Status = CheckReplayable
			} else {
				report.Status = CheckConflict
			}
			return nil
		}

		// 申请编号已被任意一条成功的期限修订占用：整次核对失败，报告为空。
		// 编号占用属于整个保管库，与本次选中的档案无关；失败过的修订不占用编号。
		if _, rec := findRevision(data, appID); rec != nil {
			return fmt.Errorf("retention: 申请编号 %s 已用于期限修订: %w",
				appID, ErrRevisionConflict)
		}

		// 申请编号尚未成功使用：按提交顺序逐份核对，每份列出全部适用阻碍。
		// 核对规则与正式销毁共用 evaluateArchive，两处结论必然一致。
		anyBlocked := false
		for _, id := range ids {
			r := evaluateArchive(data, id, req.ProcessedOn)
			if len(r.Obstructions) > 0 {
				anyBlocked = true
			}
			report.Results = append(report.Results, r)
		}

		if anyBlocked {
			report.Status = CheckBlocked
		} else {
			report.Status = CheckReady
		}
		return nil
	})
	if err != nil {
		return CheckReport{}, err
	}
	return report, nil
}
