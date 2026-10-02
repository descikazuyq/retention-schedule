package retention

import (
	"fmt"
)

// CheckRequest 是一次只做核对、不写状态的销毁申请预查。
//
// 调用者提交申请编号、处理日期和档案编号名单，得到整批申请能否办理的报告。
// 核对不销毁档案、不生成或改写清册，也不占用申请编号。
type CheckRequest struct {
	ApplicationID string
	ProcessedOn   Date
	ArchiveIDs    []string
}

// CheckStatus 描述整批核对的结论。
type CheckStatus string

const (
	// CheckOK 表示名单中全部档案均无阻碍，可以办理销毁。
	CheckOK CheckStatus = "可以办理"
	// CheckBlocked 表示名单中存在阻碍，每份档案的具体阻碍见 Archives。
	CheckBlocked CheckStatus = "存在阻碍"
	// CheckReplayable 表示申请编号已成功使用，且日期与档案集合与原申请一致，可取回原清册。
	CheckReplayable CheckStatus = "可以取回原清册"
	// CheckConflict 表示申请编号已成功使用，但本次日期或档案集合与原申请冲突。
	CheckConflict CheckStatus = "申请编号冲突"
)

// BlockerKind 描述单份档案核对中发现的阻碍类型。
// 各类原因可由调用者分别识别。
type BlockerKind string

const (
	// BlockerNotFound 表示档案未登记。
	BlockerNotFound BlockerKind = "不存在"
	// BlockerDestroyed 表示档案已经销毁。
	BlockerDestroyed BlockerKind = "已销毁"
	// BlockerNotExpired 表示按处理日期尚未到期（截止日当天才视为到期）。
	BlockerNotExpired BlockerKind = "未到期"
	// BlockerActiveFreeze 表示档案仍有未解除的冻结。
	BlockerActiveFreeze BlockerKind = "未解除冻结"
)

// Blocker 是单份档案的一项阻碍。
//
// Kind 为 BlockerActiveFreeze 时，FreezeID、Reason、FrozenOn 给出该条冻结的编号、
// 原因与冻结日期；其余 Kind 下这些字段为零值。
type Blocker struct {
	Kind     BlockerKind
	FreezeID string
	Reason   string
	FrozenOn Date
}

// ArchiveCheck 是单份档案的核对结果，按提交顺序排列。
type ArchiveCheck struct {
	ID        string
	Found     bool
	Category  string
	Start     Date
	End       Date
	Destroyed bool
	// ManifestApplicationID 是已销毁档案所属清册的申请编号；未销毁时为空。
	ManifestApplicationID string
	// ManifestProcessedOn 是已销毁档案所属清册的处理日期；未销毁时为零值。
	ManifestProcessedOn Date
	// Blockers 是该档案适用的全部阻碍，按判断顺序排列。
	// 未到期与多条未解除冻结并存时，两类问题都会列出；每条冻结的顺序沿用冻结历史。
	Blockers []Blocker
}

// CheckReport 是整批核对的结果。
//
// Status 为 CheckOK 或 CheckBlocked 时，Archives 按提交顺序给出每份档案的结果；
// Status 为 CheckReplayable 或 CheckConflict 时，Manifest 为原申请的清册。
// 报告及其中清册均为快照，调用者修改不会影响保管库中保存的历史。
type CheckReport struct {
	Status   CheckStatus
	Archives []ArchiveCheck
	Manifest *Manifest
}

// Check 只做核对，不销毁档案、不生成或改写清册、不占用申请编号。
//
// 调用者提交申请编号、处理日期和档案编号名单，得到整批申请能否办理的报告：
//   - 申请编号已成功使用且日期、档案集合与原申请一致（顺序无关）：Status 为
//     CheckReplayable，可取回原清册，这些档案即使已销毁也不判为受阻；
//   - 申请编号已成功使用但日期或档案集合与原申请冲突：Status 为 CheckConflict，
//     并附原清册；
//   - 申请编号未成功使用：按提交顺序逐份核对，已登记档案给出类别、起算日、
//     截止日和销毁状态，不存在的编号单独标明、不妨碍其他档案继续核对；
//     每份档案列出全部适用阻碍，未到期与多条未解除冻结并存时两类都会列出，
//     每条未解除冻结带上编号、原因和冻结日期，顺序沿用冻结历史。
//
// 只有所有档案均无阻碍，整批才显示可以办理。
//
// 输入无效（编号空白、日期缺失或不是真实的 YYYY-MM-DD 日期、名单为空或含重复编号，
// 包括仅因首尾空白不同而重复）时整次核对明确失败，不返回部分报告；读取本地记录
// 失败时同样明确失败，不会拿旧报告充当本次结果。核对不改变任何状态，失败后可沿用
// 同一编号继续核对或正式提交。
func (s *Store) Check(req CheckRequest) (CheckReport, error) {
	applicationID, err := requireText(req.ApplicationID, "申请编号")
	if err != nil {
		return CheckReport{}, err
	}
	if err := requireDate(req.ProcessedOn, "处理日期"); err != nil {
		return CheckReport{}, err
	}
	if len(req.ArchiveIDs) == 0 {
		return CheckReport{}, fmt.Errorf("retention: 申请 %s 的核对名单为空: %w",
			applicationID, ErrEmptyDestructionList)
	}

	// 规范化名单并检查重复（输入校验先于任何业务判断）。
	ids := make([]string, 0, len(req.ArchiveIDs))
	for _, raw := range req.ArchiveIDs {
		id, err := requireText(raw, "档案编号")
		if err != nil {
			return CheckReport{}, err
		}
		ids = append(ids, id)
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			return CheckReport{}, fmt.Errorf("retention: 申请 %s 的核对名单中档案 %s 重复: %w",
				applicationID, id, ErrDuplicateSelection)
		}
		seen[id] = struct{}{}
	}

	var report CheckReport
	err = s.view(func(data *storeData) error {
		// 申请编号已成功使用：取回原清册或标明冲突，不再逐份判断阻碍。
		if existing, ok := data.Manifests[applicationID]; ok {
			m := manifestFromRecord(existing)
			report.Manifest = &m
			if sameApplication(existing, req.ProcessedOn, seen) {
				report.Status = CheckReplayable
			} else {
				report.Status = CheckConflict
			}
			return nil
		}

		report.Status = CheckOK
		report.Archives = make([]ArchiveCheck, 0, len(ids))
		for _, id := range ids {
			ac := ArchiveCheck{ID: id}
			ar, ok := data.Archives[id]
			if !ok {
				ac.Found = false
				ac.Blockers = []Blocker{{Kind: BlockerNotFound}}
				report.Status = CheckBlocked
				report.Archives = append(report.Archives, ac)
				continue
			}

			ac.Found = true
			ac.Category = ar.Category
			ac.Start = ar.Start
			ac.End = ar.End
			ac.Destroyed = ar.Destroyed

			if ar.Destroyed {
				ac.ManifestApplicationID = ar.ManifestID
				if rec, ok := data.Manifests[ar.ManifestID]; ok {
					ac.ManifestProcessedOn = rec.ProcessedOn
				}
				ac.Blockers = []Blocker{{Kind: BlockerDestroyed}}
				report.Status = CheckBlocked
				report.Archives = append(report.Archives, ac)
				continue
			}

			blocked := false
			// 到期判断沿用销毁规则：处理日期达到截止日当天即到期。
			if req.ProcessedOn.Before(ar.End) {
				ac.Blockers = append(ac.Blockers, Blocker{Kind: BlockerNotExpired})
				blocked = true
			}
			// 任一未解除冻结都会阻止销毁；已解除记录不作为阻碍。
			// 顺序沿用冻结历史，未到期与冻结并存时两类问题都列出。
			for _, f := range ar.Freezes {
				if !f.Released {
					ac.Blockers = append(ac.Blockers, Blocker{
						Kind:     BlockerActiveFreeze,
						FreezeID: f.ID,
						Reason:   f.Reason,
						FrozenOn: f.FrozenOn,
					})
					blocked = true
				}
			}
			if blocked {
				report.Status = CheckBlocked
			}
			report.Archives = append(report.Archives, ac)
		}
		return nil
	})
	if err != nil {
		return CheckReport{}, err
	}
	return report, nil
}
