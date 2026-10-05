package retention

import (
	"bytes"
	"encoding/json"
	"fmt"
)

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

// RevisionRecord 是一条已成功办理的保管截止日修订记录。
//
// OldEnd 是修订前生效的截止日，NewEnd 是修订后生效的截止日；
// RevisedOn 仅用于记录办理日期，提交成功即采用新期限。
type RevisionRecord struct {
	RevisionID string
	ArchiveID  string
	OldEnd     Date
	NewEnd     Date
	RevisedOn  Date
	Reason     string
}

// ArchiveHistory 是按档案编号核对时看到的完整历史。
type ArchiveHistory struct {
	ID       string
	Category string
	Start    Date
	// InitialEnd 是最初登记时的保管截止日，不随修订改变。
	InitialEnd Date
	// RetentionEnd 是当前生效的保管截止日，等于最后一次成功修订的新截止日；
	// 从未修订过时与 InitialEnd 相同。
	RetentionEnd          Date
	Destroyed             bool
	ManifestApplicationID string
	// Revisions 包含全部截止日修订记录，按成功办理顺序排列。
	Revisions []RevisionRecord
	// Freezes 包含全部冻结及解除历史，按登记顺序排列。
	Freezes []FreezeRecord
	// ActiveFreezes 仅包含尚未解除的冻结。
	ActiveFreezes []FreezeRecord
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

// 以下类型是持久化到磁盘的数据结构，仅在包内使用。

type archiveRecord struct {
	ID         string            `json:"id"`
	Category   string            `json:"category"`
	Start      Date              `json:"start"`
	End        Date              `json:"end"`
	InitialEnd Date              `json:"initial_end,omitempty"`
	Destroyed  bool              `json:"destroyed"`
	ManifestID string            `json:"manifest_id,omitempty"`
	Freezes    []*freezeRecord   `json:"freezes"`
	Revisions  []*revisionRecord `json:"revisions,omitempty"`
}

// revisionRecord 是一条已保存的截止日修订；文本字段保存时均已去除首尾空白。
type revisionRecord struct {
	ID        string `json:"id"`
	OldEnd    Date   `json:"old_end"`
	NewEnd    Date   `json:"new_end"`
	RevisedOn Date   `json:"revised_on"`
	Reason    string `json:"reason"`
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
	ApplicationID string `json:"application_id"`
	// ProcessedOn 用指针保存：清册缺少处理日期或保存为 null 时保持 nil，
	// 由 load 的语义校验按损坏报告（并能指出申请编号），而不是落入 JSON 解析错误。
	// 通过校验的已关闭清册必然带有有效处理日期。
	ProcessedOn *Date           `json:"processed_on"`
	Entries     []manifestEntry `json:"entries"`
}

type manifestEntry struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Start    Date   `json:"start"`
	End      Date   `json:"end"`
}

// archiveMap 以档案编号为键保存登记记录，并在从 JSON 解码时守住
// “一个档案编号只能登记一次”的要求。
//
// 直接把对象解码进普通 map 时，encoding/json 对同名键只会保留最后一个
// 值：保存内容中若写了两份同编号登记，前一份会被静默覆盖（例如前一份带着
// 未解除的诉讼冻结、后一份冻结列表为空，读取后只剩后一份，销毁资格会被
// 误判）。UnmarshalJSON 逐键解码并按解码后的实际文本核对编号，出现重复时
// 返回 duplicateArchiveIDError，由 load 统一包装成 ErrCorruptState。
type archiveMap map[string]*archiveRecord

// UnmarshalJSON 逐键解码档案集合，发现重复编号即报错。
//
// 编号按 JSON 字符串解码后的实际文本识别：直接写出的 "A-1" 与通过 Unicode
// 转义写出的同一编号（键写作 "A-" 紧接 1 的 Unicode 转义形式）仍算重复；不同档案登记内容里出现相同
// 的字段名（如各自的 "id"）属于正常格式，与此无关——那些是登记记录内部的
// 字段，不是档案集合对象的键。键的解码经过与值内文本一致的 JSON 转义处理，
// 因此不会因保存文字看起来不同而放行重复编号。
func (m *archiveMap) UnmarshalJSON(raw []byte) error {
	// archives 保存为 null 与字段缺失等价，按空集合处理（与既有兼容行为一致）。
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		*m = archiveMap{}
		return nil
	}
	// 先用只保留键文本的解码拿到稳定的键序，再逐键把原始片段解码成记录，
	// 保证同编号键出现两次时不是“后者覆盖前者”，而是明确报错。
	dec := json.NewDecoder(bytes.NewReader(raw))
	startTok, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := startTok.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("retention: archives 字段不是 JSON 对象")
	}

	out := archiveMap{}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		id, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("retention: archives 字段的档案编号不是 JSON 字符串")
		}
		var rec archiveRecord
		if err := dec.Decode(&rec); err != nil {
			return err
		}
		if _, dup := out[id]; dup {
			return &duplicateArchiveIDError{ID: id}
		}
		out[id] = &rec
	}
	// 消费对象结束括号，确保整个值恰好是一个对象。
	if _, err := dec.Token(); err != nil {
		return err
	}
	*m = out
	return nil
}

// duplicateArchiveIDError 表示保存的档案集合中同一编号出现了多次。
// 它在包内由 load 统一包装成可由 ErrCorruptState 识别的错误。
type duplicateArchiveIDError struct {
	ID string
}

func (e *duplicateArchiveIDError) Error() string {
	return "retention: 档案编号 " + e.ID + " 在保存的档案集合中登记了多次"
}

// manifestMap 以申请编号为键保存已关闭清册，并在从 JSON 解码时守住
// “一个已成功的销毁申请编号只能对应一份已关闭清册”的要求。
//
// 直接把对象解码进普通 map 时，encoding/json 对同名键只会保留最后一个
// 值：保存内容中若写了两份同申请编号的清册，前一份会被静默覆盖。由于两份
// 清册可能各自都符合档案归属、条目快照、期限与处理日期规则，仅留下后一份
// 时保管库仍能打开，取回的清册随保存顺序改变——已成功的销毁申请只能对应
// 一份已关闭清册，这种重复保存不是正常的申请重试（幂等重放应取回唯一的原
// 清册，绝不应在保存层产生两份记录）。UnmarshalJSON 逐键解码并按解码后的
// 实际文本核对申请编号，出现重复时返回 duplicateApplicationIDError，由
// load 统一包装成 ErrCorruptState。
type manifestMap map[string]*manifestRecord

// UnmarshalJSON 逐键解码清册集合，发现同一申请编号对应两份清册即报错。
//
// 编号按 JSON 字符串解码后的实际文本识别：直接写出的 "APP-1" 与通过
// Unicode 转义写出的同一编号仍算重复；不同清册记录内部都带有处理日期、
// 条目、档案编号等同名字段（如各自的 "application_id"、"entries"）属于
// 正常保存格式，与此无关——那些是清册记录内部的字段，不是清册集合对象的
// 键。键的解码经过与值内文本一致的 JSON 转义处理，因此不会因保存文字
// 看起来不同而放行重复编号。
func (m *manifestMap) UnmarshalJSON(raw []byte) error {
	// manifests 保存为 null 与字段缺失等价，按空集合处理（与既有兼容行为一致）。
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		*m = manifestMap{}
		return nil
	}
	// 先用只保留键文本的解码拿到稳定的键序，再逐键把原始片段解码成清册，
	// 保证同编号键出现两次时不是“后者覆盖前者”，而是明确报错。
	dec := json.NewDecoder(bytes.NewReader(raw))
	startTok, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := startTok.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("retention: manifests 字段不是 JSON 对象")
	}

	out := manifestMap{}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		id, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("retention: manifests 字段的申请编号不是 JSON 字符串")
		}
		var rec manifestRecord
		if err := dec.Decode(&rec); err != nil {
			return err
		}
		if _, dup := out[id]; dup {
			return &duplicateApplicationIDError{ID: id}
		}
		out[id] = &rec
	}
	// 消费对象结束括号，确保整个值恰好是一个对象。
	if _, err := dec.Token(); err != nil {
		return err
	}
	*m = out
	return nil
}

// duplicateApplicationIDError 表示保存的清册集合中同一申请编号对应了多份
// 清册。它在包内由 load 统一包装成可由 ErrCorruptState 识别的错误。
type duplicateApplicationIDError struct {
	ID string
}

func (e *duplicateApplicationIDError) Error() string {
	return "retention: 申请编号 " + e.ID + " 在保存的清册集合中对应了多份清册"
}

type storeData struct {
	Version   int         `json:"version"`
	Archives  archiveMap  `json:"archives"`
	Manifests manifestMap `json:"manifests"`
}

func newStoreData() *storeData {
	return &storeData{
		Version:   1,
		Archives:  archiveMap{},
		Manifests: manifestMap{},
	}
}
