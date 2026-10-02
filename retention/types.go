package retention

// ArchiveRecord 是按档案编号核对时返回的完整历史视图。
type ArchiveRecord struct {
	// Number 是唯一档案编号。
	Number string
	// Category 是类别。
	Category string
	// StartDate 是起算日。
	StartDate Date
	// Deadline 是保管截止日。
	Deadline Date
	// Destroyed 表示是否已销毁。
	Destroyed bool
	// Inventory 是销毁时生成的清册申请编号；未销毁时为空。
	Inventory string
	// Freezes 是全部冻结及解除历史，按登记顺序排列。
	Freezes []FreezeRecord
}

// FreezeRecord 是一条冻结记录及其解除历史。
type FreezeRecord struct {
	// Number 是冻结编号，在同一档案内唯一。
	Number string
	// Reason 是冻结原因。
	Reason string
	// Date 是冻结日期。
	Date Date
	// Unfreeze 是解除记录；未解除时为 nil。
	Unfreeze *UnfreezeRecord
}

// UnfreezeRecord 是一条解除记录。
type UnfreezeRecord struct {
	// Reason 是解除原因。
	Reason string
	// Date 是解除日期。
	Date Date
}

// Unfrozen 报告该冻结是否已解除。
func (f FreezeRecord) Unfrozen() bool { return f.Unfreeze != nil }

// Inventory 是销毁成功后生成的已关闭清册。
// 清册内容在生成时即固定，此后不可更改。
type Inventory struct {
	// ApplicationNumber 是唯一申请编号。
	ApplicationNumber string
	// ProcessDate 是本次处理日期。
	ProcessDate Date
	// Items 是清册内各档案当时的登记快照。
	Items []InventoryItem
}

// InventoryItem 是清册内单个档案的快照。
type InventoryItem struct {
	Number    string
	Category  string
	StartDate Date
	Deadline  Date
}
