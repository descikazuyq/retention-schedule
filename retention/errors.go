package retention

import "errors"

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
