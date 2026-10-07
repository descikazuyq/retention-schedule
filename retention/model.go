package retention

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

// UnmarshalJSON 逐字段解码一份档案的登记记录，守住“同一份档案的保存内容中
// 冻结列表 freezes 与修订列表 revisions 各自最多只能出现一次”的要求。
//
// 直接把登记记录解码进结构体时，encoding/json 对同名字段只保留最后一个值：
// 同一份档案的保存内容中若写了两次 freezes，先保存的冻结历史会被后一次静默
// 替换（例如先保存包含未解除诉讼冻结的 freezes，后面又保存一个空的 freezes，
// 读取后历史里原冻结消失，到期后的销毁前核对可能误报可以办理）；写了两次
// revisions 时，先保存的修订历史同样会被后一次静默替换（例如第一处保存着
// 先延长后缩短回最初期限的两条修订、第二处是空列表，读取后当前截止日仍与
// 最初期限一致、保管库仍能打开，但两次修订消失，已占用的修订编号也可能被
// 重新用于新业务）。这样的记录不能被当成没有冻结或没有修订的正常档案使用。
//
// 对象的框架解析、字段名按 JSON 解码实际文本的大小写不敏感识别、受“只能
// 出现一次”限制字段的重复拒绝（重复检查先于值解码）、未知字段跳过等共同
// 规则统一由 decodeJSONObject 维护，这里只声明这份档案自己的两个受限字段
// （freezes、revisions）、各自的值解码目标与重复错误。同一列表第二次出现
// 时立即返回对应的重复字段错误（errDuplicateFreezesField /
// errDuplicateRevisionsField）：两处列表内容是否完全一致、是否分别保存不同
// 记录、其中一处是否为空列表或 null，都不影响拒绝——绝不合并两处列表、不
// 挑选其中一份，拒绝结果与两处的保存顺序无关。
//
// 检查只针对这份档案自己的保存内容：不同档案各自的冻结列表、修订列表互不
// 影响，冻结与修订记录内部的同名字段（id、原因、日期）也不是列表字段，不会
// 被误判；其余字段保持既有读取行为（同名字段沿用 encoding/json 的后者覆盖
// 前者，未知字段跳过）。
func (r *archiveRecord) UnmarshalJSON(raw []byte) error {
	return decodeJSONObject(raw,
		fmt.Errorf("retention: 档案登记记录不是 JSON 对象"),
		fmt.Errorf("retention: 档案登记记录的字段名不是 JSON 字符串"),
		[]uniqueField{
			{
				name:      "freezes",
				duplicate: errDuplicateFreezesField,
				decode:    func(dec *json.Decoder) error { return dec.Decode(&r.Freezes) },
			},
			{
				name:   "id",
				decode: func(dec *json.Decoder) error { return dec.Decode(&r.ID) },
			},
			{
				name:   "category",
				decode: func(dec *json.Decoder) error { return dec.Decode(&r.Category) },
			},
			{
				name:   "start",
				decode: func(dec *json.Decoder) error { return dec.Decode(&r.Start) },
			},
			{
				name:   "end",
				decode: func(dec *json.Decoder) error { return dec.Decode(&r.End) },
			},
			{
				name:   "initial_end",
				decode: func(dec *json.Decoder) error { return dec.Decode(&r.InitialEnd) },
			},
			{
				name:   "destroyed",
				decode: func(dec *json.Decoder) error { return dec.Decode(&r.Destroyed) },
			},
			{
				name:   "manifest_id",
				decode: func(dec *json.Decoder) error { return dec.Decode(&r.ManifestID) },
			},
			{
				name:      "revisions",
				duplicate: errDuplicateRevisionsField,
				decode:    func(dec *json.Decoder) error { return dec.Decode(&r.Revisions) },
			},
		})
}

// errDuplicateFreezesField 表示同一份档案的保存内容中冻结列表 freezes 出现了
// 两次或更多次。它在解码时尚不知道档案编号（编号是集合对象的键），由
// archiveMap 在解码出错的键处包装成 duplicateFreezesFieldError 并附上编号，
// 再由 load 统一包装成可由 ErrCorruptState 识别的错误。
var errDuplicateFreezesField = errors.New("retention: 冻结列表 freezes 在单份档案的保存内容中出现了多次")

// duplicateFreezesFieldError 表示档案 ID 的保存内容中冻结列表 freezes 出现了
// 两次或更多次。错误说明重复的是冻结列表本身，而不是某个冻结编号重复。
type duplicateFreezesFieldError struct {
	ID string
}

func (e *duplicateFreezesFieldError) Error() string {
	return "retention: 档案 " + e.ID + " 的保存内容中冻结列表 freezes 出现了多次"
}

// errDuplicateRevisionsField 表示同一份档案的保存内容中修订列表 revisions 出现了
// 两次或更多次。它在解码时尚不知道档案编号（编号是集合对象的键），由
// archiveMap 在解码出错的键处包装成 duplicateRevisionsFieldError 并附上编号，
// 再由 load 统一包装成可由 ErrCorruptState 识别的错误。
var errDuplicateRevisionsField = errors.New("retention: 修订列表 revisions 在单份档案的保存内容中出现了多次")

// duplicateRevisionsFieldError 表示档案 ID 的保存内容中修订列表 revisions 出现了
// 两次或更多次。错误说明重复的是修订列表本身，而不是某个修订编号重复。
type duplicateRevisionsFieldError struct {
	ID string
}

func (e *duplicateRevisionsFieldError) Error() string {
	return "retention: 档案 " + e.ID + " 的保存内容中修订列表 revisions 出现了多次"
}

// revisionRecord 是一条已保存的截止日修订；文本字段保存时均已去除首尾空白。
type revisionRecord struct {
	ID     string `json:"id"`
	OldEnd Date   `json:"old_end"`
	NewEnd Date   `json:"new_end"`
	// RevisedOn 用指针保存：修订缺少修订日期或保存为 null 时保持 nil
	// （encoding/json 对 null 指针不调用 Date.UnmarshalJSON），由 load 的
	// 语义校验按损坏报告（并能指出档案编号与修订编号），而不是落入 JSON
	// 解析错误或被零值日期顶替；一旦给出字符串，Date.UnmarshalJSON 按 JSON
	// 解码后的实际文本校验真实日期，非法文本（含不是合法日历日期的值）直接
	// 使解码失败。通过校验的修订必然带有有效修订日期。
	RevisedOn *Date  `json:"revised_on"`
	Reason    string `json:"reason"`
}

type freezeRecord struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
	// FrozenOn 用指针保存：冻结缺少冻结日期或保存为 null 时保持 nil，
	// 由 load 的语义校验按损坏报告（并能指出档案编号与冻结编号），而不是
	// 落入 JSON 解析错误或被零值日期顶替。通过校验的冻结必然带有有效冻结日期。
	FrozenOn      *Date  `json:"frozen_on"`
	Released      bool   `json:"released"`
	ReleaseReason string `json:"release_reason,omitempty"`
	// ReleasedOn 用指针保存：字段缺失或保存为 null 时保持 nil（encoding/json
	// 对 null 指针不调用 Date.UnmarshalJSON），由 load 的语义校验按损坏报告；
	// 一旦给出字符串，Date.UnmarshalJSON 按 JSON 解码后的实际文本校验真实
	// 日期，非法文本（含转义后出现加号、空白等）直接使解码失败。
	ReleasedOn *Date `json:"released_on,omitempty"`
}

// UnmarshalJSON 逐字段解码一条冻结记录，守住“同一条冻结记录的保存内容中解除
// 标记 released 最多只能出现一次”的要求。
//
// 直接把冻结记录解码进结构体时，encoding/json 对同名字段只保留最后一个值：
// 同一条冻结的保存内容中若先写了 released:false、后面又写 released:true 并
// 带上合法的解除原因和解除日期，读取会采用后一个标记——档案到期后，销毁前
// 核对可能误报可以办理，正式销毁也会忽略这条冻结。这样的记录不能被当成解除
// 状态明确的正常冻结使用。
//
// 对象的框架解析、字段名按 JSON 解码实际文本的大小写不敏感识别、受“只能
// 出现一次”限制字段的重复拒绝（重复检查先于值解码）、未知字段跳过等共同
// 规则统一由 decodeJSONObject 维护，这里只声明这条冻结自己唯一受限的字段
// released 及其重复错误。解除标记第二次出现时立即返回
// duplicateReleasedFieldError：两处取值相反、完全相同，或其中一处为 null，
// 都不影响拒绝——绝不选取其中一个值继续使用，拒绝结果与两处的保存顺序无关。
//
// 冻结编号可能写在重复的解除标记之后，因此先单独预解码一次取出编号填入重复
// 错误（编号缺失、为空白等问题仍由 load 的语义校验按损坏报告）。检查只针对
// 这条冻结自己的保存内容：同一档案不同冻结、不同档案各自的解除标记不合在
// 一起计数；其余字段保持既有读取行为（同名字段沿用 encoding/json 的后者覆盖
// 前者，未知字段跳过）。
func (r *freezeRecord) UnmarshalJSON(raw []byte) error {
	// 冻结编号可能写在重复的解除标记之后，先单独取出用于错误信息；
	// 编号缺失、为空白等问题由 load 的语义校验按损坏报告。
	var probe struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &probe)
	return decodeJSONObject(raw,
		fmt.Errorf("retention: 冻结记录不是 JSON 对象"),
		fmt.Errorf("retention: 冻结记录的字段名不是 JSON 字符串"),
		[]uniqueField{
			{
				name:      "released",
				duplicate: &duplicateReleasedFieldError{FreezeID: probe.ID},
				decode:    func(dec *json.Decoder) error { return dec.Decode(&r.Released) },
			},
			{
				name:   "id",
				decode: func(dec *json.Decoder) error { return dec.Decode(&r.ID) },
			},
			{
				name:   "reason",
				decode: func(dec *json.Decoder) error { return dec.Decode(&r.Reason) },
			},
			{
				name:   "frozen_on",
				decode: func(dec *json.Decoder) error { return dec.Decode(&r.FrozenOn) },
			},
			{
				name:   "release_reason",
				decode: func(dec *json.Decoder) error { return dec.Decode(&r.ReleaseReason) },
			},
			{
				name:   "released_on",
				decode: func(dec *json.Decoder) error { return dec.Decode(&r.ReleasedOn) },
			},
		})
}

// duplicateReleasedFieldError 表示同一条冻结记录的保存内容中解除标记 released
// 出现了两次或更多次。错误说明重复的是解除标记 released 本身，而不是冻结编号
// 或冻结列表重复。FreezeID 在 freezeRecord 解码时填入（冻结编号可能写在重复
// 标记之后，由单独的一次预解码取出），ArchiveID 由 archiveMap 在解码出错的
// 键处补上，再由 load 统一包装成可由 ErrCorruptState 识别的错误。
type duplicateReleasedFieldError struct {
	ArchiveID string
	FreezeID  string
}

func (e *duplicateReleasedFieldError) Error() string {
	return "retention: 档案 " + e.ArchiveID + " 的冻结 " + e.FreezeID + " 的保存内容中解除标记 released 出现了多次"
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

// uniqueField 声明某个 JSON 对象里“最多只能出现一次”的一个字段。
//
// name 是按 JSON 解码后的实际文本识别的字段名（大小写不敏感匹配，JSON 转义
// 已由解码器归一）；字段第二次及以后出现时，对象解码器返回 duplicate；出现
// 一次时把该字段的值交给 decode 解码到调用方自己的目标。没有列入的字段不受
// 唯一性限制，继续沿用既有读取方式（未知字段跳过）。
type uniqueField struct {
	name      string
	duplicate error
	decode    func(dec *json.Decoder) error
}

// decodeJSONObject 把读取保存内容时三类 JSON 对象——最外层保存记录
// （storeData）、单份档案的登记内容（archiveRecord）、单条冻结记录
// （freezeRecord）——共同的逐字段读取规则集中维护一份：
//
//   - 去首尾空白后等于 null 时不报错、目标保持零值，沿用 encoding/json 对
//     null 的既有行为（缺项等问题由 load 的语义校验按损坏报告）；
//   - 值必须恰好是一个 JSON 对象：首个 token 不是对象左括号即返回 notObject；
//   - 逐个读取字段名：字段名必须是 JSON 字符串，否则返回 badKey。字段名按
//     JSON 字符串解码后的实际文本识别——Token() 已完成 \uXXXX 等转义解码，
//     直接写出的字段名与转义写法解码后相同即同一字段；再以 strings.EqualFold
//     做大小写不敏感匹配，现有大小写写法单独出现继续可读，混用它们重复出现
//     也算同一字段；
//   - fields 声明的每个字段各自最多只能出现一次：重复检查先于值解码，字段
//     第二次出现时立即返回该字段自己的 duplicate，绝不让后一个值把先保存的
//     内容静默替换；两处取值相同、相反、为空集合/空列表或 null 都同样拒绝，
//     拒绝结果与保存顺序无关；
//   - 其余未声明字段（未知字段）与既有行为一致：原样跳过、不校验，因此本
//     整理不扩大哪些字段必须唯一，也不改变未受限字段的读取方式；
//   - 最后消费对象结束括号，保证整个值恰好是一个对象。
//
// 各层不同的部分由参数传入：notObject、badKey 给出该层的框架错误信息；
// fields 列出该层受“只能出现一次”限制的字段、各自的值解码目标与重复错误
// （重复错误需能说明重复的是哪个集合、列表或标记，并由调用方在需要时补上
// 档案、冻结编号）。
func decodeJSONObject(raw []byte, notObject, badKey error, fields []uniqueField) error {
	// 记录保存为 null 时保持零值（与 encoding/json 对 null 的既有行为一致）。
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	startTok, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := startTok.(json.Delim); !ok || delim != '{' {
		return notObject
	}
	seen := make([]bool, len(fields))
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyTok.(string)
		if !ok {
			return badKey
		}
		matched := false
		for i := range fields {
			if !strings.EqualFold(key, fields[i].name) {
				continue
			}
			// 重复检查必须先于解码：即使第二处为空集合、空列表、null，或取
			// 相反的值，也不能让后一个值顶替先保存的内容后继续使用。
			if seen[i] {
				return fields[i].duplicate
			}
			seen[i] = true
			if err := fields[i].decode(dec); err != nil {
				return err
			}
			matched = true
			break
		}
		if matched {
			continue
		}
		// 未知字段与既有行为一致：跳过不校验。
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return err
		}
	}
	// 消费对象结束括号，确保整个值恰好是一个对象。
	if _, err := dec.Token(); err != nil {
		return err
	}
	return nil
}

// unmarshalKeyedRecordMap 按档案集合与清册集合共同的读取规则解码一个以编号
// 为键、逐项保存记录的 JSON 对象；两类集合共同的集合格式、编号解码与重复键
// 核对只在这里维护一份。
//
// 共同读取规则：
//   - 集合保存为 null 时按空集合处理（字段缺失时根本不会进入这里），保留既有
//     的空集合兼容行为；
//   - 值必须恰好是一个 JSON 对象，第一个 token 不是对象左括号即报错；
//   - 逐个读取对象键：键必须是 JSON 字符串，编号按 JSON 字符串解码后的实际
//     文本识别——直接写出的编号与通过 Unicode 转义写出、解码后相同的编号是
//     同一个键。检查只针对集合对象自身的键，记录内部的同名字段（如每份登记
//     的 "id"、每份清册的 "application_id" 与条目里的 "id"）不是集合键，不会
//     被当成重复编号；
//   - 逐键把对应值解码成一条记录（记录内容的结构由类型参数 T 决定），同一
//     解码后编号出现第二次时立即返回 duplicate(id)：绝不以后一份覆盖前一份，
//     两份内容是否完全相同、期限、冻结状态或处理日期有何差别都不影响拒绝；
//   - 最后消费对象结束括号，保证整个值恰好是一个对象；对象之后再拼接任何内容
//     由外层 json.Unmarshal 的“整值恰好一个 JSON 值”校验拒绝。
//
// 两类集合各自不同、不在这里合并的部分由参数传入：fieldName 与 idKind 只用于
// 错误信息，T 决定每条记录的内容结构，duplicate 按解码出的重复编号构造该集合
// 自己的重复错误（档案集合说明重复登记，清册集合说明同一申请对应多份清册），
// 再由 load 统一包装成 ErrCorruptState。recordErr 在逐键解码单条记录出错时
// 用该记录的查找编号包装错误（档案集合用它给冻结列表、修订列表重复错误附上
// 档案编号），为 nil 时记录解码错误原样返回。
func unmarshalKeyedRecordMap[T any](raw []byte, fieldName, idKind string, duplicate func(id string) error, recordErr func(id string, err error) error) (map[string]*T, error) {
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
		return nil, fmt.Errorf("retention: %s 字段不是 JSON 对象", fieldName)
	}

	out := map[string]*T{}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		id, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("retention: %s 字段的%s不是 JSON 字符串", fieldName, idKind)
		}
		var rec T
		if err := dec.Decode(&rec); err != nil {
			if recordErr != nil {
				return nil, recordErr(id, err)
			}
			return nil, err
		}
		if _, dup := out[id]; dup {
			return nil, duplicate(id)
		}
		out[id] = &rec
	}
	// 消费对象结束括号，确保整个值恰好是一个对象。
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return out, nil
}

// archiveMap 以档案编号为键保存登记记录，并在从 JSON 解码时守住
// “一个档案编号只能登记一次”的要求。
//
// 直接把对象解码进普通 map 时，encoding/json 对同名键只会保留最后一个
// 值：保存内容中若写了两份同编号登记，前一份会被静默覆盖（例如前一份带着
// 未解除的诉讼冻结、后一份冻结列表为空，读取后只剩后一份，销毁资格会被
// 误判）。除键不重复外，每条记录的身份还必须自洽：用于找到该记录的键与
// 记录内容里的 id 必须同为非空白文本且逐字相同。键与 id 是两处独立保存的
// 文本，普通解码不会核对它们，可能出现挂在 A-1 名下、id 却写成 A-2，或
// id 缺失、为 null、空串、只有空白的记录——按 A-1 查到的历史会标着 A-2，
// 销毁还可能把 A-2 写进清册。这层检查不在 UnmarshalJSON 中完成（解码只
// 负责拒绝重复键），而由 load 的 validateArchiveIdentity 在进入任何业务
// 操作前统一核对，命中即包装成 ErrCorruptState：绝不做去空白、大小写
// 归一化、改号或补写缺失编号。
type archiveMap map[string]*archiveRecord

// UnmarshalJSON 逐键解码档案集合，发现重复编号即报 duplicateArchiveIDError。
// 集合格式、编号按 JSON 解码实际文本识别、记录内部同名字段不算集合键等
// 共同读取规则统一由 unmarshalKeyedRecordMap 维护，这里只传入档案集合自己的
// 字段名、编号称谓与重复错误。逐键解码单份登记记录时，若记录自己的保存内容
// 中冻结列表 freezes 或修订列表 revisions 出现了两次或更多次
// （archiveRecord.UnmarshalJSON 返回 errDuplicateFreezesField /
// errDuplicateRevisionsField），或某条冻结自己的保存内容中解除标记 released
// 出现了两次或更多次（freezeRecord.UnmarshalJSON 返回
// duplicateReleasedFieldError），在这里用该记录的查找编号包装成
// duplicateFreezesFieldError / duplicateRevisionsFieldError，或为
// duplicateReleasedFieldError 补上档案编号——重复检查只针对这份档案、这条
// 冻结自己的保存内容，不同档案各自的列表、不同冻结各自的解除标记不会合在
// 一起计数。
func (m *archiveMap) UnmarshalJSON(raw []byte) error {
	out, err := unmarshalKeyedRecordMap[archiveRecord](raw, "archives", "档案编号",
		func(id string) error { return &duplicateArchiveIDError{ID: id} },
		func(id string, err error) error {
			if errors.Is(err, errDuplicateFreezesField) {
				return &duplicateFreezesFieldError{ID: id}
			}
			if errors.Is(err, errDuplicateRevisionsField) {
				return &duplicateRevisionsFieldError{ID: id}
			}
			// 单条冻结记录自己的保存内容中解除标记 released 出现了两次或更多次
			// 时，在这里用该记录的查找编号补上档案编号——重复检查只针对这条
			// 冻结自己的保存内容，不同冻结、不同档案各自的解除标记不合在一起
			// 计数。
			var dupReleased *duplicateReleasedFieldError
			if errors.As(err, &dupReleased) {
				dupReleased.ArchiveID = id
				return dupReleased
			}
			return err
		})
	if err != nil {
		return err
	}
	*m = archiveMap(out)
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
// load 统一包装成 ErrCorruptState。除键不重复外，每份清册的身份还必须
// 自洽：用于找到该清册的键与清册内容里的 application_id 必须同为非空白
// 文本且逐字相同——可能出现挂在 APP-1 名下、application_id 却写成 APP-2，
// 或 application_id 缺失、为 null、空串、只有空白的记录，此时按 APP-1
// 取回的清册会标着 APP-2，重提原申请也会拿到编号不符的清册。这层检查
// 不在 UnmarshalJSON 中完成（解码只负责拒绝重复键），而由 load 的
// validateManifestIdentity 在进入任何业务操作前统一核对，命中即包装成
// ErrCorruptState：绝不做去空白、大小写归一化、用查找编号补写清册、
// 以清册内容改号或删除冲突记录。
type manifestMap map[string]*manifestRecord

// UnmarshalJSON 逐键解码清册集合，发现同一申请编号对应两份清册即报
// duplicateApplicationIDError。集合格式、编号按 JSON 解码实际文本识别、
// 记录内部同名字段不算集合键等共同读取规则统一由 unmarshalKeyedRecordMap
// 维护，这里只传入清册集合自己的字段名、编号称谓与重复错误。
func (m *manifestMap) UnmarshalJSON(raw []byte) error {
	out, err := unmarshalKeyedRecordMap[manifestRecord](raw, "manifests", "申请编号",
		func(id string) error { return &duplicateApplicationIDError{ID: id} }, nil)
	if err != nil {
		return err
	}
	*m = manifestMap(out)
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

// UnmarshalJSON 逐键解码保存记录的最外层对象，守住“同一份保存记录的最外层
// 最多只能出现一次档案集合，也最多只能出现一次清册集合”的要求。
//
// 直接把最外层对象解码进结构体时，encoding/json 对同名字段只会保留最后一个
// 值：保存内容中若写了两次 archives，前一次保存的档案集合会被后一次静默替换
// （例如第一处保存着带未解除诉讼冻结的 A-1，第二处保存成没有冻结的同号档案，
// 读取后只剩后者，销毁资格会被误判为可以办理）；写了两次 manifests 时前一份
// 清册集合同样会被后一份整批替换——两份清册可能各自都符合档案归属、条目快照、
// 期限与处理日期规则（例如 APP-1 的清册收录截止日为 2025-01-10 的已销毁档案，
// 两处处理日期分别为 2025-01-10 和 2025-01-11，两个日期都满足到期规则），仅
// 留下后一份时保管库仍能打开，取回的处理日期却随保存顺序变化。已关闭清册的
// 历史不能这样被覆盖。
//
// 对象的框架解析、字段名按 JSON 解码实际文本的大小写不敏感识别、受“只能
// 出现一次”限制字段的重复拒绝（重复检查先于值解码）、未知字段跳过等共同
// 规则统一由 decodeJSONObject 维护，这里只声明最外层自己的两个受限集合字段
// （archives、manifests）、各自的值解码目标与重复错误。同一集合字段第二次
// 出现时立即返回对应的重复字段错误：两处集合内容是否完全一致、是否只含不同
// 编号（清册两处分别只含不同申请）、其中一处是否为空对象或 null，都不影响
// 拒绝——绝不合并、不取最后一份，也不按处理日期、收录档案或哪份保留了更多
// 冻结挑选，拒绝结果与两处的保存顺序无关。
//
// 检查只针对最外层的集合字段：各份档案登记内容里各自出现的 id、日期、冻结等
// 同名字段是正常保存格式，多份清册内部各自带有的申请编号、处理日期和条目字段
// 也是正常保存格式，不会被误判为最外层集合重复；集合内部同一编号出现两次仍
// 分别由 archiveMap.UnmarshalJSON 与 manifestMap.UnmarshalJSON 按既有规则拒绝。
func (d *storeData) UnmarshalJSON(raw []byte) error {
	return decodeJSONObject(raw,
		fmt.Errorf("retention: 保存记录不是 JSON 对象"),
		fmt.Errorf("retention: 保存记录最外层的字段名不是 JSON 字符串"),
		[]uniqueField{
			{
				name:      "archives",
				duplicate: &duplicateArchivesFieldError{},
				decode:    func(dec *json.Decoder) error { return dec.Decode(&d.Archives) },
			},
			{
				name: "manifests",
				// 重复检查必须先于解码：即使第二处为空对象或 null，也不能让它
				// 把第一处清册集合整批替换成空集合后继续使用。
				duplicate: &duplicateManifestsFieldError{},
				decode:    func(dec *json.Decoder) error { return dec.Decode(&d.Manifests) },
			},
			{
				name:   "version",
				decode: func(dec *json.Decoder) error { return dec.Decode(&d.Version) },
			},
		})
}

// duplicateArchivesFieldError 表示保存记录的最外层出现了两次或更多次档案
// 集合 archives。它在包内由 load 统一包装成可由 ErrCorruptState 识别的错误；
// 错误说明重复的是档案集合本身，不涉及任何具体档案编号。
type duplicateArchivesFieldError struct{}

func (e *duplicateArchivesFieldError) Error() string {
	return "retention: 保存记录最外层的档案集合 archives 出现了多次"
}

// duplicateManifestsFieldError 表示保存记录的最外层出现了两次或更多次清册
// 集合 manifests。它在包内由 load 统一包装成可由 ErrCorruptState 识别的错误；
// 错误说明重复的是清册集合本身，而不是某个申请编号在集合内重复——即使两处
// 分别只含不同申请，重复的也只是集合字段。
type duplicateManifestsFieldError struct{}

func (e *duplicateManifestsFieldError) Error() string {
	return "retention: 保存记录最外层的清册集合 manifests 出现了多次"
}

func newStoreData() *storeData {
	return &storeData{
		Version:   1,
		Archives:  archiveMap{},
		Manifests: manifestMap{},
	}
}
