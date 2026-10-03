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
// 申请编号已被本保管库内任意一条成功的期限修订使用时，编号占用属于整个保管库、
// 与本次选中的档案无关，核对在逐份检查之前即整体失败，返回的错误可用
// errors.Is(err, ErrRevisionConflict) 识别，错误信息说明该编号已用于期限修订
// 并带出冲突编号；此时返回的报告为空，不列出逐份档案结果，也不附清册。
// 只有成功修订才占用编号：失败的修订不留记录，核对不会仅因编号曾被提交而拒绝。
//
// 到期与冻结判断与 Destroy 完全一致：处理日期达到截止日当天即到期，
// 任一未解除冻结都会阻止销毁，已解除记录不作为阻碍。
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
		// 申请编号已成功用于销毁：只判断能否取回原清册或编号冲突，两者都附原清册。
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

		// 申请编号已被任意一条成功的期限修订占用：编号占用属于整个保管库，
		// 与本次选中的档案无关（甲档案用 R-1 成功修订后，即使只核对乙档案，
		// R-1 也不能使用；甲档案后来再次修订或已销毁也不释放原编号）。
		// 此时在逐份核对之前整体失败：报告为空，不列逐份结果也不附清册。
		// findRevision 只找得到成功修订：失败的修订不留记录，不会因此被拒。
		if _, rec := findRevision(data, appID); rec != nil {
			return fmt.Errorf("retention: 申请编号 %s 已用于期限修订，冲突编号 %s: %w",
				appID, rec.ID, ErrRevisionConflict)
		}

		report = CheckReport{
			ApplicationID: appID,
			ProcessedOn:   req.ProcessedOn,
			Results:       make([]ArchiveCheckResult, 0, len(ids)),
		}

		// 申请编号尚未成功使用：按提交顺序逐份核对，每份列出全部适用阻碍。
		anyBlocked := false
		for _, id := range ids {
			r := ArchiveCheckResult{ID: id, Obstructions: []CheckObstruction{}}
			ar, ok := data.Archives[id]
			if !ok {
				// 不存在的编号单独标明，不中断其他档案的核对。
				r.Obstructions = append(r.Obstructions, CheckObstruction{Kind: ObstructionMissing})
				anyBlocked = true
				report.Results = append(report.Results, r)
				continue
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
					obs.ProcessedOn = rec.ProcessedOn
				} else {
					obs.ManifestApplicationID = ar.ManifestID
				}
				r.Obstructions = append(r.Obstructions, obs)
				anyBlocked = true
			} else {
				// 未到期与未解除冻结可能同时成立，两类问题都要列出。
				if req.ProcessedOn.Before(ar.End) {
					r.Obstructions = append(r.Obstructions, CheckObstruction{Kind: ObstructionNotExpired})
					anyBlocked = true
				}
				// 每条未解除冻结各成一条阻碍，顺序沿用冻结历史（登记顺序）。
				for _, f := range ar.Freezes {
					if f.Released {
						continue
					}
					r.Obstructions = append(r.Obstructions, CheckObstruction{
						Kind: ObstructionActiveFreeze,
						Freeze: ActiveFreezeInfo{
							FreezeID: f.ID,
							Reason:   f.Reason,
							FrozenOn: f.FrozenOn,
						},
					})
					anyBlocked = true
				}
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
