package retention

import (
	"errors"
	"fmt"
)

// 业务规则错误。调用方可用 errors.Is 判断失败类别，具体错误信息会附带上编号等上下文。
var (
	// ErrNotFound 表示档案或相关记录不存在。
	ErrNotFound = errors.New("retention: 档案不存在")
	// ErrDuplicateID 表示档案编号已经登记过。
	ErrDuplicateID = errors.New("retention: 档案编号重复")
	// ErrDestroyed 表示档案已经销毁，不能再执行该操作。
	ErrDestroyed = errors.New("retention: 档案已销毁")
	// ErrBlankField 表示必填的编号、类别或原因为空白。
	ErrBlankField = errors.New("retention: 字段不能为空白")
	// ErrInvalidDate 表示日期缺失或不是真实的日历日期。
	ErrInvalidDate = errors.New("retention: 日期无效")
	// ErrRetentionEndBeforeStart 表示保管截止日早于起算日。
	ErrRetentionEndBeforeStart = errors.New("retention: 保管截止日不能早于起算日")
	// ErrReleaseBeforeFreeze 表示解除日期早于冻结日期。
	ErrReleaseBeforeFreeze = errors.New("retention: 解除日期不能早于冻结日期")
	// ErrDuplicateFreezeID 表示同一档案内冻结编号重复。
	ErrDuplicateFreezeID = errors.New("retention: 冻结编号在同一档案内重复")
	// ErrFreezeNotFound 表示该档案下没有对应编号的冻结。
	ErrFreezeNotFound = errors.New("retention: 冻结不存在")
	// ErrFreezeAlreadyReleased 表示冻结已经解除，不能重复解除。
	ErrFreezeAlreadyReleased = errors.New("retention: 冻结已经解除")
	// ErrEmptyDestructionList 表示销毁名单为空。
	ErrEmptyDestructionList = errors.New("retention: 销毁名单不能为空")
	// ErrDuplicateSelection 表示同一次销毁申请中档案编号重复。
	ErrDuplicateSelection = errors.New("retention: 销毁名单中档案编号重复")
	// ErrNotDestroyable 表示名单中存在未到期或仍有未解除冻结的档案。
	ErrNotDestroyable = errors.New("retention: 存在不能销毁的档案")
	// ErrNotExpired 表示档案尚未到保管截止日。截止日当天才视为到期。
	ErrNotExpired = errors.New("retention: 档案尚未到期")
	// ErrActiveFreeze 表示档案仍有未解除的冻结。
	ErrActiveFreeze = errors.New("retention: 档案存在未解除的冻结")
	// ErrApplicationMismatch 表示申请编号已成功使用，但本次日期或档案集合与原申请不一致。
	ErrApplicationMismatch = errors.New("retention: 申请编号与已成功的申请不一致")
	// ErrArchiveAlreadyOnManifest 表示档案已经出现在另一份成功清册中。
	ErrArchiveAlreadyOnManifest = errors.New("retention: 档案已在其他销毁清册中")
	// ErrDuplicateRevisionID 表示修订编号已成功使用，但本次提交内容与原修订不一致。
	ErrDuplicateRevisionID = errors.New("retention: 修订编号已存在且提交内容不一致")
	// ErrRevisionEndMismatch 表示提交的原截止日与保管库当前保存值不一致，期限已被他人修改。
	ErrRevisionEndMismatch = errors.New("retention: 保管截止日已被修改")
	// ErrRevisionEndUnchanged 表示修订后的新截止日与提交的原截止日相同。
	ErrRevisionEndUnchanged = errors.New("retention: 修订后的截止日与原截止日相同")
	// ErrRevisionIDUsedByManifest 表示该修订编号已被销毁申请使用，两个编号空间互不占用。
	ErrRevisionIDUsedByManifest = errors.New("retention: 修订编号已被销毁申请使用")
	// ErrApplicationIDUsedByRevision 表示该销毁申请编号已被修订记录使用。
	ErrApplicationIDUsedByRevision = errors.New("retention: 销毁申请编号已被修订记录使用")
)

// InvalidDateError 说明 ParseDate 收到的值不是合法的 YYYY-MM-DD 真实日期。
type InvalidDateError struct {
	Value string
}

func (e *InvalidDateError) Error() string {
	return "retention: 日期无效，应为 YYYY-MM-DD 且真实存在: " + e.Value
}

// Is 支持 errors.Is(err, ErrInvalidDate)。
func (e *InvalidDateError) Is(target error) bool { return target == ErrInvalidDate }

// RevisionEndMismatchError 说明提交的原截止日与保管库当前保存值不一致，
// 期限已被他人修改。错误中给出当前截止日，调用方可据此提示或重新提交。
// 用 errors.Is(err, ErrRevisionEndMismatch) 判别。
type RevisionEndMismatchError struct {
	ArchiveID string
	Submitted Date
	Current   Date
}

func (e *RevisionEndMismatchError) Error() string {
	return fmt.Sprintf("retention: 档案 %s 的保管截止日已变化：当前截止日为 %s，提交的原截止日为 %s",
		e.ArchiveID, e.Current, e.Submitted)
}

// Is 支持 errors.Is(err, ErrRevisionEndMismatch)。
func (e *RevisionEndMismatchError) Is(target error) bool { return target == ErrRevisionEndMismatch }
