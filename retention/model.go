package retention

// 以下类型是对外的只读视图，由查询方法复制内部状态生成，
// 调用方修改它们不会影响保管库中保存的记录。

// FreezeRecord 描述一条冻结及其解除信息。
// 未解除时 Released 为 false，ReleasedOn 为零值，ReleaseReason 为空。
type FreezeRecord struct {
	ID            string
	Reason        string
	FrozenOn      Date
	Released      bool
	ReleaseReason string
	ReleasedOn    Date
}

// ManifestEntry 是清册关闭瞬间档案登记内容的快照。
type ManifestEntry struct {
	ID       string
	Category string
	Start    Date
	End      Date
}

// Manifest 是一份已经关闭、内容不可更改的销毁清册。
type Manifest struct {
	ApplicationID string
	ProcessedOn   Date
	Entries       []ManifestEntry
}

// ArchiveHistory 是按档案编号核对时看到的完整历史。
type ArchiveHistory struct {
	ID                    string
	Category              string
	Start                 Date
	// RegisteredEnd 是登记时的最初截止日，此后修订只改变当前截止日，不改变它。
	RegisteredEnd         Date
	RetentionEnd          Date
	Destroyed             bool
	ManifestApplicationID string
	// Freezes 包含全部冻结及解除历史，按登记顺序排列。
	Freezes []FreezeRecord
	// ActiveFreezes 仅包含尚未解除的冻结。
	ActiveFreezes []FreezeRecord
	// Revisions 包含全部保管截止日修订记录，按成功办理顺序排列。
	Revisions []RevisionRecord
	// Manifest 是该档案销毁时生成的已关闭清册；未销毁时为 nil。
	Manifest *Manifest
}

// CheckStatus 是整批销毁前核对报告的总体结论。
type CheckStatus string

const (
	// CheckReady 表示申请编号尚未成功使用，且名单中每份档案都没有阻碍，可以办理销毁。
	CheckReady CheckStatus = "可以办理"
	// CheckBlocked 表示申请编号尚未成功使用，但名单中至少一份档案存在阻碍。
	CheckBlocked CheckStatus = "存在阻碍"
	// CheckReplayable 表示申请编号已成功使用，本次日期与档案集合与原申请相同，可以取回原清册。
	CheckReplayable CheckStatus = "可以取回原清册"
	// CheckConflict 表示申请编号已成功使用，但本次日期或档案集合与原申请不一致。
	CheckConflict CheckStatus = "申请编号冲突"
)

// ObstructionKind 标识一份档案不能办理销毁的具体原因类别，调用者可逐一识别。
type ObstructionKind string

const (
	// ObstructionMissing 表示该编号在保管库中不存在。
	ObstructionMissing ObstructionKind = "不存在"
	// ObstructionDestroyed 表示该档案已经销毁，并附所属清册信息。
	ObstructionDestroyed ObstructionKind = "已销毁"
	// ObstructionNotExpired 表示按处理日期档案尚未到保管截止日（截止日当天才到期）。
	ObstructionNotExpired ObstructionKind = "未到期"
	// ObstructionActiveFreeze 表示档案仍有一条未解除的冻结；每份未解除冻结对应一条阻碍。
	ObstructionActiveFreeze ObstructionKind = "仍有未解除冻结"
)

// ActiveFreezeInfo 是核对时看到的一条未解除冻结，字段取自冻结历史登记顺序。
type ActiveFreezeInfo struct {
	FreezeID string
	Reason   string
	FrozenOn Date
}

// CheckObstruction 描述一份档案的一条适用阻碍。
//
// 一份未到期且有多条未解除冻结的档案会同时列出未到期阻碍和
// 每条冻结各一条阻碍；冻结阻碍按冻结历史登记顺序排列。
// 仅 ObstructionDestroyed 时 ManifestApplicationID 与 ProcessedOn 有意义，
// 给出所属清册的申请编号和处理日期。
type CheckObstruction struct {
	Kind                  ObstructionKind
	Freeze                ActiveFreezeInfo
	ManifestApplicationID string
	ProcessedOn           Date
}

// ArchiveCheckResult 是名单中一份档案的核对结果，按调用者提交顺序出现。
//
// 已登记档案无论能否办理都带类别、起算日、截止日和销毁状态；
// 编号不存在时 Exists 为 false，登记字段为零值，且只带一条 ObstructionMissing 阻碍，
// 不影响名单中其他档案继续核对。
type ArchiveCheckResult struct {
	ID        string
	Exists    bool
	Category  string
	Start     Date
	End       Date
	Destroyed bool
	// Obstructions 列出全部适用阻碍；为空表示该档案按当前状态可以销毁。
	Obstructions []CheckObstruction
}

// CheckReport 是一次只做核对的完整结果。
//
// 报告中的档案结果与清册全部来自同一次已保存状态读取，不会混入其他程序
// 办理前后的不同结果。报告及其中清册均为复制生成的只读视图，调用者修改
// 不影响保管库中保存的历史。报告只说明核对当时的情况，不预占状态：
// 之后新增冻结或其他程序先完成销毁，正式提交仍按最新状态判断。
type CheckReport struct {
	ApplicationID string
	ProcessedOn   Date
	Status        CheckStatus
	// Results 按提交顺序列出每份档案的核对结果（申请编号未成功使用时）。
	Results []ArchiveCheckResult
	// Manifest 仅在状态为可以取回原清册或申请编号冲突时附带原清册，其余情况为 nil。
	Manifest *Manifest
}

// RevisionInput 是修订档案保管截止日所需的信息。
//
// 修订编号与原因为必填文本（去首尾空白后不得为空）；修订日期沿用真实日历日期规则，
// 仅用于记录，提交成功即采用新期限。新截止日不能早于起算日，也不能等于提交的原截止日。
// 提交的原截止日是调用者看到的当前截止日，必须与保管库当前保存值一致，否则因期限已变化而失败。
type RevisionInput struct {
	RevisionID  string
	ArchiveID   string
	OriginalEnd Date
	NewEnd      Date
	RevisedOn   Date
	Reason      string
}

// RevisionRecord 描述一条成功的保管截止日修订记录。
//
// 每条记录带修订编号、所属档案、修订前后截止日、修订日期与原因。
// 返回值是复制生成的只读视图，调用者修改不影响保管库中保存的历史。
type RevisionRecord struct {
	RevisionID  string
	ArchiveID   string
	OriginalEnd Date
	NewEnd      Date
	RevisedOn   Date
	Reason      string
}

// 以下类型是持久化到磁盘的数据结构，仅在包内使用。

type archiveRecord struct {
	ID            string            `json:"id"`
	Category      string            `json:"category"`
	Start         Date              `json:"start"`
	End           Date              `json:"end"`
	RegisteredEnd Date              `json:"registered_end"`
	Destroyed     bool              `json:"destroyed"`
	ManifestID    string            `json:"manifest_id,omitempty"`
	Freezes       []*freezeRecord   `json:"freezes"`
	Revisions     []*revisionRecord `json:"revisions,omitempty"`
}

type revisionRecord struct {
	RevisionID  string `json:"revision_id"`
	ArchiveID   string `json:"archive_id"`
	OriginalEnd Date   `json:"original_end"`
	NewEnd      Date   `json:"new_end"`
	RevisedOn   Date   `json:"revised_on"`
	Reason      string `json:"reason"`
}

type freezeRecord struct {
	ID            string `json:"id"`
	Reason        string `json:"reason"`
	FrozenOn      Date   `json:"frozen_on"`
	Released      bool   `json:"released"`
	ReleaseReason string `json:"release_reason,omitempty"`
	ReleasedOn    *Date  `json:"released_on,omitempty"`
}

type manifestRecord struct {
	ApplicationID string          `json:"application_id"`
	ProcessedOn   Date            `json:"processed_on"`
	Entries       []manifestEntry `json:"entries"`
}

type manifestEntry struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Start    Date   `json:"start"`
	End      Date   `json:"end"`
}

type storeData struct {
	Version   int                        `json:"version"`
	Archives  map[string]*archiveRecord  `json:"archives"`
	Manifests map[string]*manifestRecord `json:"manifests"`
	Revisions map[string]*revisionRecord `json:"revisions,omitempty"`
}

func newStoreData() *storeData {
	return &storeData{
		Version:   1,
		Archives:  map[string]*archiveRecord{},
		Manifests: map[string]*manifestRecord{},
		Revisions: map[string]*revisionRecord{},
	}
}
