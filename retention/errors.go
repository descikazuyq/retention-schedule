package retention

import "errors"

// 本包的所有错误都可用 errors.Is 判定。
var (
	// ErrBlankField 表示必填字段为空白（空串或仅空白字符）。
	ErrBlankField = errors.New("retention: 字段不能为空白")

	// ErrInvalidDate 表示日期格式不是 YYYY-MM-DD 或日期不存在。
	ErrInvalidDate = errors.New("retention: 日期无效")

	// ErrDeadlineBeforeStart 表示保管截止日早于起算日。
	ErrDeadlineBeforeStart = errors.New("retention: 保管截止日不能早于起算日")

	// ErrDuplicateNumber 表示登记时档案编号已存在。
	ErrDuplicateNumber = errors.New("retention: 档案编号已存在")

	// ErrArchiveNotFound 表示按编号找不到档案。
	ErrArchiveNotFound = errors.New("retention: 档案不存在")

	// ErrArchiveDestroyed 表示档案已销毁（不能再冻结，也不能重复销毁）。
	ErrArchiveDestroyed = errors.New("retention: 档案已销毁")

	// ErrDuplicateFreezeNumber 表示同一档案内冻结编号重复。
	ErrDuplicateFreezeNumber = errors.New("retention: 冻结编号已存在")

	// ErrFreezeNotFound 表示冻结不存在或不属于该档案。
	ErrFreezeNotFound = errors.New("retention: 冻结不存在")

	// ErrAlreadyUnfrozen 表示冻结已经解除，不能重复解除。
	ErrAlreadyUnfrozen = errors.New("retention: 冻结已解除")

	// ErrUnfreezeBeforeFreeze 表示解除日期早于冻结日期。
	ErrUnfreezeBeforeFreeze = errors.New("retention: 解除日期不能早于冻结日期")

	// ErrEmptyDestroyList 表示销毁名单为空。
	ErrEmptyDestroyList = errors.New("retention: 销毁名单为空")

	// ErrDuplicateInList 表示销毁名单内编号重复。
	ErrDuplicateInList = errors.New("retention: 销毁名单中编号重复")

	// ErrNotExpired 表示档案在处理日尚未到期（截止日当天算到期）。
	ErrNotExpired = errors.New("retention: 档案未到期")

	// ErrFrozen 表示档案存在尚未解除的冻结，不能销毁。
	ErrFrozen = errors.New("retention: 档案存在未解除冻结")

	// ErrApplicationConflict 表示沿用已成功的申请编号，但处理日期或档案集合不一致。
	ErrApplicationConflict = errors.New("retention: 申请编号已存在但内容不一致")

	// ErrInventoryNotFound 表示清册不存在。
	ErrInventoryNotFound = errors.New("retention: 清册不存在")

	// ErrStoreClosed 表示保管位置已关闭。
	ErrStoreClosed = errors.New("retention: 保管位置已关闭")
)
