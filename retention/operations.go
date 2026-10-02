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
			ID:       id,
			Category: category,
			Start:    in.Start,
			End:      in.End,
			Freezes:  nil,
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
		h = ArchiveHistory{
			ID:                    ar.ID,
			Category:              ar.Category,
			Start:                 ar.Start,
			RetentionEnd:          ar.End,
			Destroyed:             ar.Destroyed,
			ManifestApplicationID: ar.ManifestID,
			Freezes:               make([]FreezeRecord, 0, len(ar.Freezes)),
		}
		for _, fr := range ar.Freezes {
			view := freezeFromRecord(fr)
			h.Freezes = append(h.Freezes, view)
			if !fr.Released {
				h.ActiveFreezes = append(h.ActiveFreezes, view)
			}
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
