package retention

import (
	"fmt"
	"sort"
	"strings"
)

// RegisterInput 是登记一份档案所需的信息。
//
// 编号和类别不能是空白；日期必须是 ParseDate 能解析的真实日期；
// 截止日可以与起算日相同，但不能更早。
type RegisterInput struct {
	ID       string
	Category string
	Start    Date
	End      Date
}

// FreezeInput 是为档案新增一条冻结所需的信息。
type FreezeInput struct {
	ArchiveID string
	FreezeID  string
	Reason    string
	FrozenOn  Date
}

// ReleaseInput 是解除一条冻结所需的信息。
type ReleaseInput struct {
	ArchiveID  string
	FreezeID   string
	Reason     string
	ReleasedOn Date
}

// DestructionRequest 是一次销毁申请。
//
// 一次申请可以选择多个档案。申请编号在所有成功申请中唯一，
// 处理日期即判断档案是否到期所依据的日期。
type DestructionRequest struct {
	ApplicationID string
	ProcessedOn   Date
	ArchiveIDs    []string
}

// CheckRequest 是销毁前一次只做核对的请求，字段含义与 DestructionRequest 相同。
//
// 核对不销毁任何档案、不生成或改写清册，也不占用申请编号：
// 核对失败或发现阻碍后，仍可沿用同一编号继续核对或正式提交。
type CheckRequest struct {
	ApplicationID string
	ProcessedOn   Date
	ArchiveIDs    []string
}

// normalizeText 去除首尾空白；空白字段统一按空白处理，避免同值异写。
func normalizeText(s string) string { return strings.TrimSpace(s) }

func requireText(value, name string) (string, error) {
	v := normalizeText(value)
	if v == "" {
		return "", fmt.Errorf("retention: %s不能为空白: %w", name, ErrBlankField)
	}
	return v, nil
}

func requireDate(d Date, name string) error {
	if d.IsZero() {
		return fmt.Errorf("retention: %s缺失或无效，应为 YYYY-MM-DD: %w", name, ErrInvalidDate)
	}
	return nil
}

// Register 登记一份档案。
//
// 编号重复或任一输入无效时明确失败，并且不写盘，已有记录不受影响。
func (s *Store) Register(in RegisterInput) error {
	id, err := requireText(in.ID, "档案编号")
	if err != nil {
		return err
	}
	category, err := requireText(in.Category, "类别")
	if err != nil {
		return err
	}
	if err := requireDate(in.Start, "起算日"); err != nil {
		return err
	}
	if err := requireDate(in.End, "保管截止日"); err != nil {
		return err
	}
	if in.End.Before(in.Start) {
		return fmt.Errorf("retention: 档案 %s 的保管截止日 %s 早于起算日 %s: %w",
			id, in.End, in.Start, ErrRetentionEndBeforeStart)
	}
	return s.mutate(func(data *storeData) error {
		if _, exists := data.Archives[id]; exists {
			return fmt.Errorf("retention: 档案编号 %s 已登记: %w", id, ErrDuplicateID)
		}
		data.Archives[id] = &archiveRecord{
			ID:            id,
			Category:      category,
			Start:         in.Start,
			End:           in.End,
			RegisteredEnd: in.End,
			Freezes:       nil,
		}
		return nil
	})
}

// Freeze 为档案登记一条冻结。
//
// 冻结编号在同一档案内唯一。档案不存在、已销毁、冻结编号重复或输入无效时失败。
func (s *Store) Freeze(in FreezeInput) error {
	archiveID, err := requireText(in.ArchiveID, "档案编号")
	if err != nil {
		return err
	}
	freezeID, err := requireText(in.FreezeID, "冻结编号")
	if err != nil {
		return err
	}
	reason, err := requireText(in.Reason, "冻结原因")
	if err != nil {
		return err
	}
	if err := requireDate(in.FrozenOn, "冻结日期"); err != nil {
		return err
	}
	return s.mutate(func(data *storeData) error {
		ar, ok := data.Archives[archiveID]
		if !ok {
			return fmt.Errorf("retention: 档案 %s 不存在，不能新增冻结: %w", archiveID, ErrNotFound)
		}
		if ar.Destroyed {
			return fmt.Errorf("retention: 档案 %s 已销毁，不能新增冻结: %w", archiveID, ErrDestroyed)
		}
		for _, f := range ar.Freezes {
			if f.ID == freezeID {
				return fmt.Errorf("retention: 档案 %s 内冻结编号 %s 已存在: %w",
					archiveID, freezeID, ErrDuplicateFreezeID)
			}
		}
		ar.Freezes = append(ar.Freezes, &freezeRecord{
			ID:       freezeID,
			Reason:   reason,
			FrozenOn: in.FrozenOn,
		})
		return nil
	})
}

// Release 解除一条冻结。
//
// 必须填写解除日期与原因，解除日期不能早于该冻结的冻结日期。
// 解除只在原记录上标记，不删除冻结记录，也不影响同一档案的其他冻结。
// 不能解除不存在或已经解除的冻结。
func (s *Store) Release(in ReleaseInput) error {
	archiveID, err := requireText(in.ArchiveID, "档案编号")
	if err != nil {
		return err
	}
	freezeID, err := requireText(in.FreezeID, "冻结编号")
	if err != nil {
		return err
	}
	reason, err := requireText(in.Reason, "解除原因")
	if err != nil {
		return err
	}
	if err := requireDate(in.ReleasedOn, "解除日期"); err != nil {
		return err
	}
	return s.mutate(func(data *storeData) error {
		ar, ok := data.Archives[archiveID]
		if !ok {
			return fmt.Errorf("retention: 档案 %s 不存在: %w", archiveID, ErrNotFound)
		}
		var fr *freezeRecord
		for _, f := range ar.Freezes {
			if f.ID == freezeID {
				fr = f
				break
			}
		}
		if fr == nil {
			return fmt.Errorf("retention: 档案 %s 下冻结 %s 不存在: %w",
				archiveID, freezeID, ErrFreezeNotFound)
		}
		if fr.Released {
			return fmt.Errorf("retention: 档案 %s 的冻结 %s 已经解除，不能重复解除: %w",
				archiveID, freezeID, ErrFreezeAlreadyReleased)
		}
		if in.ReleasedOn.Before(fr.FrozenOn) {
			return fmt.Errorf("retention: 解除日期 %s 早于冻结 %s 的冻结日期 %s: %w",
				in.ReleasedOn, freezeID, fr.FrozenOn, ErrReleaseBeforeFreeze)
		}
		fr.Released = true
		fr.ReleaseReason = reason
		releasedOn := in.ReleasedOn
		fr.ReleasedOn = &releasedOn
		return nil
	})
}

// Revise 修订档案的保管截止日。
//
// 调用者发现登记的期限需要调整时，可以对尚未销毁的档案延长或缩短截止日，
// 不必重新登记；编号、类别与起算日均不可改动。被冻结的档案也允许修订期限，
// 但修订不能解除冻结，期限缩短后仍须满足原有冻结规则才能销毁。
//
// 每次提交包含修订编号、档案编号、调用者看到的原截止日、新截止日、修订日期和原因。
// 修订编号与原因去首尾空白后不得为空；日期沿用真实日历日期规则。新截止日不能早于
// 起算日，也不能等于所提交的原截止日。修订日期仅用于记录，提交成功即采用新期限。
//
// 首次办理时，所提交的原截止日必须与当前保存值相同，否则返回可区分的期限已变化错误
// （ErrRevisionEndMismatch）并提供当前截止日，不能覆盖他人的修改。
//
// 修订编号在同一保管库的全部档案之间唯一，且与销毁申请编号互不占用。同编号且提交
// 内容相同的重试只返回第一次成功的记录，不增加历史；即使后来再次改过期限或档案已被
// 销毁，这次重试仍能取回原记录。沿用成功编号改变任何一项内容时返回编号冲突
// （ErrDuplicateRevisionID）。失败过的编号仍可用于重新提交。
//
// 任何失败都不改变期限或留下修订历史。返回值是复制生成的只读视图，调用者修改不影响
// 保管库中保存的记录。
func (s *Store) Revise(in RevisionInput) (RevisionRecord, error) {
	revisionID, err := requireText(in.RevisionID, "修订编号")
	if err != nil {
		return RevisionRecord{}, err
	}
	archiveID, err := requireText(in.ArchiveID, "档案编号")
	if err != nil {
		return RevisionRecord{}, err
	}
	reason, err := requireText(in.Reason, "修订原因")
	if err != nil {
		return RevisionRecord{}, err
	}
	if err := requireDate(in.RevisedOn, "修订日期"); err != nil {
		return RevisionRecord{}, err
	}
	if err := requireDate(in.OriginalEnd, "原截止日"); err != nil {
		return RevisionRecord{}, err
	}
	if err := requireDate(in.NewEnd, "新截止日"); err != nil {
		return RevisionRecord{}, err
	}
	if in.NewEnd.Equal(in.OriginalEnd) {
		return RevisionRecord{}, fmt.Errorf("retention: 档案 %s 修订后的新截止日 %s 与原截止日相同: %w",
			archiveID, in.NewEnd, ErrRevisionEndUnchanged)
	}

	var result RevisionRecord
	err = s.mutate(func(data *storeData) error {
		// 幂等：修订编号已成功使用时，内容完全相同则返回第一次成功的记录，否则编号冲突。
		// 取回原记录不依赖档案当前状态，即使期限后来被再次修改或档案已销毁也不受影响。
		if existing, ok := data.Revisions[revisionID]; ok {
			if !sameRevision(existing, in, archiveID, reason) {
				return fmt.Errorf("retention: 修订编号 %s 已成功使用，本次提交内容与原修订不一致: %w",
					revisionID, ErrDuplicateRevisionID)
			}
			result = revisionFromRecord(existing)
			return nil
		}
		// 修订编号与销毁申请编号互不占用。
		if _, ok := data.Manifests[revisionID]; ok {
			return fmt.Errorf("retention: 修订编号 %s 已被销毁申请使用: %w",
				revisionID, ErrRevisionIDUsedByManifest)
		}
		ar, ok := data.Archives[archiveID]
		if !ok {
			return fmt.Errorf("retention: 档案 %s 不存在，不能修订保管截止日: %w",
				archiveID, ErrNotFound)
		}
		if ar.Destroyed {
			return fmt.Errorf("retention: 档案 %s 已销毁，不能修订保管截止日: %w",
				archiveID, ErrDestroyed)
		}
		if in.NewEnd.Before(ar.Start) {
			return fmt.Errorf("retention: 档案 %s 修订后的保管截止日 %s 早于起算日 %s: %w",
				archiveID, in.NewEnd, ar.Start, ErrRetentionEndBeforeStart)
		}
		// 乐观核对：提交的原截止日必须与当前保存值一致，避免覆盖他人的修改。
		if !in.OriginalEnd.Equal(ar.End) {
			return &RevisionEndMismatchError{
				ArchiveID: archiveID,
				Submitted: in.OriginalEnd,
				Current:   ar.End,
			}
		}
		rec := &revisionRecord{
			RevisionID:  revisionID,
			ArchiveID:   archiveID,
			OriginalEnd: in.OriginalEnd,
			NewEnd:      in.NewEnd,
			RevisedOn:   in.RevisedOn,
			Reason:      reason,
		}
		data.Revisions[revisionID] = rec
		ar.Revisions = append(ar.Revisions, rec)
		ar.End = in.NewEnd
		result = revisionFromRecord(rec)
		return nil
	})
	if err != nil {
		return RevisionRecord{}, err
	}
	return result, nil
}

// sameRevision 判断已成功修订与本次提交的全部内容是否一致。
// 文本字段在调用入口已去首尾空白，这里直接比较规范化后的值。
func sameRevision(rec *revisionRecord, in RevisionInput, archiveID, reason string) bool {
	return rec.ArchiveID == archiveID &&
		rec.Reason == reason &&
		rec.OriginalEnd.Equal(in.OriginalEnd) &&
		rec.NewEnd.Equal(in.NewEnd) &&
		rec.RevisedOn.Equal(in.RevisedOn)
}

func revisionFromRecord(rec *revisionRecord) RevisionRecord {
	return RevisionRecord{
		RevisionID:  rec.RevisionID,
		ArchiveID:   rec.ArchiveID,
		OriginalEnd: rec.OriginalEnd,
		NewEnd:      rec.NewEnd,
		RevisedOn:   rec.RevisedOn,
		Reason:      rec.Reason,
	}
}

// Destroy 办理一次销毁申请。
//
// 只有名单中全部档案都存在、按处理日期已到期（处理日期不早于截止日，
// 截止日当天即到期）、未销毁且没有未解除冻结时，才会同时完成销毁，
// 并生成一份内容此后不可更改的已关闭清册。
//
// 名单为空、名单内编号重复或任一档案不符合条件时，整次申请失败，
// 不会留下部分销毁或清册。
//
// 同一申请编号此前已经成功办理时：处理日期与档案集合完全相同（顺序无关）
// 则返回原清册；沿用编号但改变日期或档案集合则失败。失败过的申请
// 在条件改变后可以用同一编号再次提交。
func (s *Store) Destroy(req DestructionRequest) (Manifest, error) {
	applicationID, ids, seen, err := validateBatchInputs(req.ApplicationID, req.ProcessedOn, req.ArchiveIDs)
	if err != nil {
		return Manifest{}, err
	}

	var result Manifest
	err = s.mutate(func(data *storeData) error {
		// 已成功的申请编号：幂等返回原清册，或因内容不一致失败。
		if existing, ok := data.Manifests[applicationID]; ok {
			if !sameApplication(existing, req.ProcessedOn, seen) {
				return fmt.Errorf("retention: 申请编号 %s 已成功使用，本次日期或档案集合与原申请不一致: %w",
					applicationID, ErrApplicationMismatch)
			}
			result = manifestFromRecord(existing)
			return nil
		}
		// 申请编号与修订编号互不占用：该编号已被修订记录使用。
		if _, ok := data.Revisions[applicationID]; ok {
			return fmt.Errorf("retention: 申请编号 %s 已被修订记录使用: %w",
				applicationID, ErrApplicationIDUsedByRevision)
		}

		// 逐份校验，任一不符合条件则整体放弃（此时尚未改动任何记录）。
		for _, id := range ids {
			ar, ok := data.Archives[id]
			if !ok {
				return fmt.Errorf("retention: 档案 %s 不存在: %w", id, ErrNotFound)
			}
			if ar.Destroyed {
				return fmt.Errorf("retention: 档案 %s 已在清册 %s 中，不能再次销毁: %w",
					id, ar.ManifestID, ErrArchiveAlreadyOnManifest)
			}
			if req.ProcessedOn.Before(ar.End) {
				return fmt.Errorf("retention: 档案 %s 尚未到期（截止日 %s，处理日期 %s）: %w",
					id, ar.End, req.ProcessedOn, ErrNotExpired)
			}
			for _, f := range ar.Freezes {
				if !f.Released {
					return fmt.Errorf("retention: 档案 %s 有未解除的冻结 %s: %w",
						id, f.ID, ErrActiveFreeze)
				}
			}
		}

		// 全部满足：生成关闭清册，内容为各档案当时登记内容的快照。
		rec := &manifestRecord{
			ApplicationID: applicationID,
			ProcessedOn:   req.ProcessedOn,
		}
		ordered := append([]string(nil), ids...)
		sort.Strings(ordered)
		for _, id := range ordered {
			ar := data.Archives[id]
			rec.Entries = append(rec.Entries, manifestEntry{
				ID:       ar.ID,
				Category: ar.Category,
				Start:    ar.Start,
				End:      ar.End,
			})
			ar.Destroyed = true
			ar.ManifestID = applicationID
		}
		data.Manifests[applicationID] = rec
		result = manifestFromRecord(rec)
		return nil
	})
	if err != nil {
		return Manifest{}, err
	}
	return result, nil
}

// sameApplication 判断已成功申请与本次提交的处理日期和档案集合是否完全一致（顺序无关）。
func sameApplication(existing *manifestRecord, processedOn Date, idSet map[string]struct{}) bool {
	if !existing.ProcessedOn.Equal(processedOn) {
		return false
	}
	if len(existing.Entries) != len(idSet) {
		return false
	}
	for _, e := range existing.Entries {
		if _, ok := idSet[e.ID]; !ok {
			return false
		}
	}
	return true
}

func manifestFromRecord(rec *manifestRecord) Manifest {
	m := Manifest{
		ApplicationID: rec.ApplicationID,
		ProcessedOn:   rec.ProcessedOn,
		Entries:       make([]ManifestEntry, 0, len(rec.Entries)),
	}
	for _, e := range rec.Entries {
		m.Entries = append(m.Entries, ManifestEntry{
			ID:       e.ID,
			Category: e.Category,
			Start:    e.Start,
			End:      e.End,
		})
	}
	return m
}

func freezeFromRecord(fr *freezeRecord) FreezeRecord {
	out := FreezeRecord{
		ID:            fr.ID,
		Reason:        fr.Reason,
		FrozenOn:      fr.FrozenOn,
		Released:      fr.Released,
		ReleaseReason: fr.ReleaseReason,
	}
	if fr.ReleasedOn != nil {
		out.ReleasedOn = *fr.ReleasedOn
	}
	return out
}

// History 按档案编号核对完整历史。
//
// 结果包含登记内容、全部冻结及解除历史、当前未解除冻结、销毁状态以及
// 对应的已关闭清册。档案未登记时 found 为 false（err 为 nil），
// 这是明确的不存在结果。
func (s *Store) History(archiveID string) (h ArchiveHistory, found bool, err error) {
	id := normalizeText(archiveID)
	if id == "" {
		return ArchiveHistory{}, false, fmt.Errorf("retention: 档案编号不能为空白: %w", ErrBlankField)
	}
	err = s.view(func(data *storeData) error {
		ar, ok := data.Archives[id]
		if !ok {
			return nil
		}
		found = true
		// 旧版本保存的档案没有登记最初截止日，此时登记截止日就是当前截止日。
		registeredEnd := ar.RegisteredEnd
		if registeredEnd.IsZero() {
			registeredEnd = ar.End
		}
		h = ArchiveHistory{
			ID:                    ar.ID,
			Category:              ar.Category,
			Start:                 ar.Start,
			RegisteredEnd:         registeredEnd,
			RetentionEnd:          ar.End,
			Destroyed:             ar.Destroyed,
			ManifestApplicationID: ar.ManifestID,
			Freezes:               make([]FreezeRecord, 0, len(ar.Freezes)),
			Revisions:             make([]RevisionRecord, 0, len(ar.Revisions)),
		}
		for _, fr := range ar.Freezes {
			view := freezeFromRecord(fr)
			h.Freezes = append(h.Freezes, view)
			if !fr.Released {
				h.ActiveFreezes = append(h.ActiveFreezes, view)
			}
		}
		for _, rr := range ar.Revisions {
			h.Revisions = append(h.Revisions, revisionFromRecord(rr))
		}
		if ar.Destroyed && ar.ManifestID != "" {
			if rec, ok := data.Manifests[ar.ManifestID]; ok {
				m := manifestFromRecord(rec)
				h.Manifest = &m
			}
		}
		return nil
	})
	if err != nil {
		return ArchiveHistory{}, false, err
	}
	return h, found, nil
}

// Manifest 按申请编号查询一份已关闭清册。不存在时 found 为 false。
func (s *Store) GetManifest(applicationID string) (m Manifest, found bool, err error) {
	id := normalizeText(applicationID)
	if id == "" {
		return Manifest{}, false, fmt.Errorf("retention: 申请编号不能为空白: %w", ErrBlankField)
	}
	err = s.view(func(data *storeData) error {
		if rec, ok := data.Manifests[id]; ok {
			m = manifestFromRecord(rec)
			found = true
		}
		return nil
	})
	if err != nil {
		return Manifest{}, false, err
	}
	return m, found, nil
}
