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

// uniqueKeyCollection 标识保存的编号键集合种类，用于在同一条集合读取规则里
// 区分两类集合各自的记录内容与错误说明。
type uniqueKeyCollection int

const (
	// archiveCollection 是状态文件中的档案集合（archives 字段）。
	archiveCollection uniqueKeyCollection = iota
	// manifestCollection 是状态文件中的清册集合（manifests 字段）。
	manifestCollection
)

// field 返回该集合在状态文件中的字段名，用于格式类解析错误。
func (c uniqueKeyCollection) field() string {
	switch c {
	case archiveCollection:
		return "archives"
	case manifestCollection:
		return "manifests"
	}
	return "未知集合"
}

// keyName 返回该集合键编号的称呼，用于“键不是 JSON 字符串”类解析错误。
func (c uniqueKeyCollection) keyName() string {
	switch c {
	case archiveCollection:
		return "档案编号"
	case manifestCollection:
		return "申请编号"
	}
	return "编号"
}

// decodeUniqueKeyedObject 逐键解码一个以编号为键、编号在集合内唯一的 JSON
// 对象集合，是档案集合与清册集合共用的同一条读取规则。
//
// 直接把对象解码进普通 map 时，encoding/json 对同名键只会保留最后一个值：
// 保存内容中若用同一编号写了两份记录，前一份会被静默覆盖，读取结果随保存
// 顺序改变（档案可能被后一份无冻结登记顶替，清册可能被后一份不同处理日期
// 的记录顶替）。因此这里不用普通 map 解码，而是统一守住两类集合共有的规则：
//   - 集合保存为 null 与字段缺失等价，连同空对象一起按空集合处理（与既有
//     兼容行为一致）；
//   - 集合必须是一个 JSON 对象，键必须是 JSON 字符串；
//   - 键按 JSON 字符串解码后的实际文本识别：直接写出的编号与通过 Unicode
//     转义写出、解码后相同的编号视为同一编号；不同记录内部的同名字段（登记
//     的 id、冻结的 id，或清册的 application_id、processed_on、entries、条目
//     id）是记录内部字段，不是集合对象的键，不参与编号重复判断；
//   - 每个键把对应的原始片段解码成一条记录，同一编号出现两次时绝不以后一份
//     覆盖前一份，也不按期限、冻结状态或处理日期挑选，而是返回带集合标识的
//     uniqueKeyError，由 load 统一包装成 ErrCorruptState，并保留该集合自己的
//     错误说明（档案集合说明重复登记，清册集合说明同一申请对应多份清册）。
//
// 记录本身的内容合法性（登记身份、期限、冻结历史、清册条目等）不在此判断，
// 由各自的记录类型与 load 的后续校验负责；这里只消除两类集合在集合格式、
// 编号解码与编号唯一上的重复维护。
func decodeUniqueKeyedObject[T any](raw []byte, collection uniqueKeyCollection) (map[string]*T, error) {
	// 集合保存为 null 与字段缺失等价，按空集合处理（与既有兼容行为一致）。
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return map[string]*T{}, nil
	}
	// 先用只保留键文本的解码拿到稳定的键序，再逐键把原始片段解码成记录，
	// 保证同编号键出现两次时不是“后者覆盖前者”，而是明确报错。
	dec := json.NewDecoder(bytes.NewReader(raw))
	startTok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := startTok.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("retention: %s 字段不是 JSON 对象", collection.field())
	}

	out := map[string]*T{}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		id, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("retention: %s 字段的%s不是 JSON 字符串", collection.field(), collection.keyName())
		}
		var rec T
		if err := dec.Decode(&rec); err != nil {
			return nil, err
		}
		if _, dup := out[id]; dup {
			return nil, &uniqueKeyError{Collection: collection, ID: id}
		}
		out[id] = &rec
	}
	// 消费对象结束括号，确保整个值恰好是一个对象。
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return out, nil
}

// uniqueKeyError 表示保存的某个编号键集合中同一编号出现了多次。它只携带
// “哪个集合、哪个编号重复”这一两类集合共有的事实；各自的错误说明（档案集合
// 说明重复登记，清册集合说明同一申请对应多份清册）由 load 按 Collection 区分
// 后统一包装成可由 ErrCorruptState 识别的错误，不会退化成没有具体对象的解析
// 失败。
type uniqueKeyError struct {
	Collection uniqueKeyCollection
	ID         string
}

func (e *uniqueKeyError) Error() string {
	switch e.Collection {
	case archiveCollection:
		return "retention: 档案编号 " + e.ID + " 在保存的档案集合中登记了多次"
	case manifestCollection:
		return "retention: 申请编号 " + e.ID + " 在保存的清册集合中对应了多份清册"
	}
	return "retention: 编号 " + e.ID + " 在保存的记录集合中出现了多次"
}

// archiveMap 以档案编号为键保存登记记录。
//
// 集合格式、编号解码与“一个档案编号只能登记一次”的唯一性规则与清册集合
// 完全相同，统一由 decodeUniqueKeyedObject 守住，这里只提供档案集合自己的
// 记录类型与字段名。普通解码会让同编号键的后一份登记静默覆盖前一份（例如
// 前一份带着未解除的诉讼冻结、后一份冻结列表为空，读取后只剩后一份，销毁
// 资格会被误判）。除键不重复外，每条记录的身份还必须自洽：用于找到该记录
// 的键与记录内容里的 id 必须同为非空白文本且逐字相同。键与 id 是两处独立
// 保存的文本，普通解码不会核对它们，可能出现挂在 A-1 名下、id 却写成 A-2，
// 或 id 缺失、为 null、空串、只有空白的记录——按 A-1 查到的历史会标着 A-2，
// 销毁还可能把 A-2 写进清册。这层检查不在解码中完成（解码只负责拒绝重复
// 键），而由 load 的 validateArchiveIdentity 在进入任何业务操作前统一核对，
// 命中即包装成 ErrCorruptState：绝不做去空白、大小写归一化、改号或补写缺失
// 编号。
type archiveMap map[string]*archiveRecord

// UnmarshalJSON 逐键解码档案集合；集合格式、编号解码与重复编号检测共用
// decodeUniqueKeyedObject，命中的重复编号错误在 load 中按档案集合的说明
// （重复登记）包装成 ErrCorruptState。
func (m *archiveMap) UnmarshalJSON(raw []byte) error {
	out, err := decodeUniqueKeyedObject[archiveRecord](raw, archiveCollection)
	if err != nil {
		return err
	}
	*m = archiveMap(out)
	return nil
}

// manifestMap 以申请编号为键保存已关闭清册。
//
// 集合格式、编号解码与“一个已成功的销毁申请编号只能对应一份已关闭清册”
// 的唯一性规则与档案集合完全相同，统一由 decodeUniqueKeyedObject 守住，这里
// 只提供清册集合自己的记录类型与字段名。普通解码会让同编号键的后一份清册
// 静默覆盖前一份：两份清册可能各自都符合档案归属、条目快照、期限与处理日期
// 规则，仅留下后一份时保管库仍能打开，取回的清册却随保存顺序改变——已成功
// 的销毁申请只能对应一份已关闭清册，这种重复保存不是正常的申请重试（幂等
// 重放应取回唯一的原清册，绝不应在保存层产生两份记录）。
type manifestMap map[string]*manifestRecord

// UnmarshalJSON 逐键解码清册集合；集合格式、编号解码与重复编号检测共用
// decodeUniqueKeyedObject，命中的重复编号错误在 load 中按清册集合的说明
// （该申请编号对应多份清册）包装成 ErrCorruptState。
func (m *manifestMap) UnmarshalJSON(raw []byte) error {
	out, err := decodeUniqueKeyedObject[manifestRecord](raw, manifestCollection)
	if err != nil {
		return err
	}
	*m = manifestMap(out)
	return nil
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
