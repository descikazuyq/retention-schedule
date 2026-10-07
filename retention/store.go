package retention

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
)

// 状态文件与锁文件的固定名称，位于调用者选择的本地目录内。
const (
	stateFileName = "retention-state.json"
	lockFileName  = ".retention.lock"
)

// Store 是一个本地保管库，对应调用者选择的一个本地保存位置。
//
// 同一个目录可以被本机上的多个程序（进程）同时打开：
// 每次办理都会取得跨进程的排他文件锁，并从磁盘重新读取状态，
// 因此两个程序对同一档案的冻结、销毁一定有明确的先后顺序。
type Store struct {
	dir string

	// opMu 串行化当前 Store 实例内的办理；跨进程互斥由文件锁保证。
	opMu sync.RWMutex
}

// Open 打开（或创建）一个本地保存位置。
// 目录不存在时会创建；状态文件不存在时按空库打开，可以正常登记。
// 状态文件已存在但内容损坏（空文件、只有空白、内容为 null、合法对象后面
// 还拼接了其他内容等），或其中保存的记录不满足业务不变量（例如任一日期
// 字段不是真实的 YYYY-MM-DD 日期——按 JSON 字符串解码后的实际文本判断，
// 转义后出现加号或空白、写成数字或 null 等都算损坏；最外层出现两次或更多次
// 档案集合 archives 或清册集合 manifests——转义写法或大小写写法表示同一字段名
// 同样算重复；任一档案自己的保存内容中冻结列表 freezes 或修订列表 revisions
// 出现两次或更多次——转义写法或大小写写法表示同一字段名同样算重复；同一条
// 冻结记录的保存内容中解除标记 released 出现两次或更多次——转义写法或大小写
// 写法表示同一字段名同样算重复；任一档案缺少
// 起算日或当前生效的保管截止日、用于查找档案的编号与登记内容里的 id
// 不是同一个非空白编号（缺失、为 null、空串、只有空白或两处编号不同）、同一档案编号在保存的档案集合中重复登记、
// 当前截止日或最初截止日、任一修订的原截止日、
// 新截止日早于起算日、同一档案的冻结历史中
// 冻结编号重复（解除过的记录仍占用原编号）、任一冻结缺少冻结原因或冻结日期、
// 已解除冻结缺少解除原因或解除日期、解除日期早于冻结日期、已销毁档案与已关闭清册
// 对应不上、已销毁档案仍带未解除冻结、已关闭清册缺少处理日期、清册处理
// 日期早于所收录档案销毁时最终生效的保管截止日（提前销毁）、已关闭清册
// 条目保存的类别、起算日或截止日（须为销毁时最终生效的期限）与档案登记
// 内容不一致、修订记录衔接不上、修订历史中存在早于起算日的截止日、任一已保存
// 修订缺少非空白的修订原因或有效的修订日期（原因缺失、为 null、空串或仅含
// 空白算缺少原因，修订日期缺失或为 null 算缺少日期，已填写但不是合法日历
// 日期同样损坏）、成功修订编号在保存历史中不唯一或与已关闭
// 清册的申请编号相同、同一申请编号在保存的清册集合中对应多份清册、
// 用于找到清册的申请编号与清册内容里的 application_id 不是同一个非空白
// 编号（查找键为空白，或清册内容的 application_id 缺失、为 null、空串、
// 只有空白、两处编号逐字不同））时返回 ErrCorruptState，不会返回可继续办理的保管库，
// 已有记录保持原样，不会被清空、修补或覆盖。
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("retention: 保存位置不能为空")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("retention: 无法创建保存位置: %w", err)
	}
	s := &Store{dir: dir}
	// 提前读一次，让损坏的状态文件在打开时就暴露，而不是等到第一次办理。
	if _, err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// Close 释放保管库。当前实现没有跨操作持有的资源，保留它以稳定公开入口形态。
func (s *Store) Close() error { return nil }

func (s *Store) statePath() string { return filepath.Join(s.dir, stateFileName) }
func (s *Store) lockPath() string  { return filepath.Join(s.dir, lockFileName) }

// lock 取得跨进程排他锁，返回解锁函数。
// 每次办理各自加锁、解锁，这样同一位置也允许同进程或跨进程的多个 Store 并存。
func (s *Store) lock() (func(), error) {
	f, err := os.OpenFile(s.lockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("retention: 无法打开锁文件: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("retention: 无法取得保存位置锁: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// rlock 取得跨进程共享锁，用于只读核对。
func (s *Store) rlock() (func(), error) {
	f, err := os.OpenFile(s.lockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("retention: 无法打开锁文件: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH); err != nil {
		f.Close()
		return nil, fmt.Errorf("retention: 无法取得保存位置锁: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// load 从磁盘读取最新状态。文件不存在时返回一份空状态（尚未建立记录）。
//
// 文件已经存在时，必须完整包含且只包含一个 JSON 对象（前后允许空白）：
// 零字节或只有空白的文件、内容为 null 的文件、合法对象后面还拼接了
// 第二个 JSON 值或无法解析的文字，都判为损坏并返回 ErrCorruptState，
// 绝不只使用前一段内容，也不会改动原文件。
//
// 所有日期字段都按 JSON 字符串解码后所表示的实际文本校验真实的
// YYYY-MM-DD 日期：普通字符与 \uXXXX 等合法 JSON 转义可以混用，
// "\u0032025-01-10" 与 "2025-01-10" 表示同一个日期；转义后
// 出现加号、空白等非法字符，或日期保存为数字、对象、布尔等非字符串值，
// JSON 解码即失败，按整库损坏返回 ErrCorruptState。原本允许缺省或为
// null 的日期字段（冻结的解除日期）保持既有含义；必填日期为 null 仍由
// 后续语义校验按缺项损坏报告。
//
// JSON 能解析不代表记录合法：每条冻结保留的原始信息必须与办理冻结时的
// 要求一致——非空白的冻结原因与有效的冻结日期齐备。冻结原因缺失、为
// null、为空串或仅含空白都算缺少冻结原因；冻结日期字段缺失或为 null
// 都算缺少冻结日期（已填写的日期仍须是真实的 YYYY-MM-DD 日期）。这条
// 要求覆盖未解除与已解除的全部冻结历史，已销毁档案的冻结也不例外：
// 已解除冻结的解除日期与解除原因再完整，也不能拿解除日期代替冻结日期、
// 拿解除原因补齐冻结原因；清册归属、条目与处理日期都正确同样不能掩盖
// 缺项。缺少任一信息时整份保管库判为损坏并返回 ErrCorruptState，错误
// 信息指明档案编号、冻结编号以及缺少的是冻结原因还是冻结日期（两项同时
// 缺少时两项都说明）；绝不自动补写日期或原因、删除冻结或更改解除状态。
//
// 任何已解除冻结都必须同时带有非空白的
// 解除原因和有效的解除日期，且解除日期不早于冻结日期——与解除功能
// 办理时的要求一致。缺少任一信息或日期顺序不成立时，整份保管库同样
// 判为损坏并返回 ErrCorruptState，错误信息指明涉及的档案与冻结编号。
//
// 除键的唯一性外，每份档案的身份也必须自洽：保存的档案集合中用于查找
// 每份档案的编号（对象键）与该份登记内容里的 id 必须同为非空白文本，且是
// 逐字相同的同一个编号。JSON 能解析不代表身份一致：一条记录可以被挂在
// A-1 名下、内容里的 id 却写成 A-2，或 id 缺失、为 null、为空串、只有
// 空白；此时按 A-1 查到的历史标着 A-2，销毁还可能把 A-2 写进清册，随后
// 清册与档案对应不上。这类记录无法说明自己是哪份档案，判为损坏并返回
// ErrCorruptState，绝不交付可继续办理的保管库：两处编号不同时错误说明
// 档案编号不一致并给出查找编号与记录内编号；id 无效时指出对应的查找编号
// 与缺失位置（登记内容的 id）。比较以 JSON 解码后的实际文本为准，转义
// 写法不同但解码后相同的编号仍合法；仅大小写或首尾空白不同不算一致，
// 读取时绝不做去空白、改号或补写缺失编号（办理入口对正常输入去首尾空白
// 的行为不变）。这条规则适用于尚未销毁、被冻结和已销毁的档案，合法的
// 期限、修订历史或清册归属都不能掩盖编号矛盾。
//
// 清册集合中每份清册的身份也必须自洽：保存的清册集合中用于找到每份已
// 关闭清册的编号（对象键）与该份清册内容里的 application_id 必须同为非
// 空白文本，且是逐字相同的同一个编号。JSON 能解析不代表身份一致：一份
// 清册可以被挂在 APP-1 名下、内容里的 application_id 却写成 APP-2，或
// application_id 缺失、为 null、为空串、只有空白；此时按 APP-1 取回的
// 清册标着 APP-2，重提原申请也会拿到编号不符的清册。这类记录无法说明
// 自己属于哪次申请，判为损坏并返回 ErrCorruptState，绝不交付可继续办理
// 的保管库：两处编号不同时错误说明申请编号不一致并给出查找编号与清册
// 内容里的申请编号；application_id 无效时指出对应的查找编号与缺失位置
// （清册内容的 application_id）；用于查找的键本身为空白时同样拒绝。
// 比较以 JSON 解码后的实际文本为准，转义写法不同但解码后相同的编号仍
// 合法；仅大小写或首尾空白不同不算一致，读取时绝不做去空白、大小写
// 归一化、用查找编号补写清册或以清册内容改号（办理入口对正常输入去
// 首尾空白的行为不变）。即使档案已经销毁、归属关系正确、条目快照与处理
// 日期都合法、期限修订完整，清册的其他内容也不能掩盖编号矛盾。
//
// 同一份保存记录的最外层最多只能出现一次档案集合：普通解码对同名字段只保留
// 最后一个值，保存内容中若写了两次 archives，前一次的档案集合会被后一次静默
// 替换（例如第一处保存着带未解除诉讼冻结的 A-1，第二处保存成没有冻结的同号
// 档案，读取后只剩后者，销毁资格会被误判为可以办理）。因此解码时逐个核对最
// 外层字段名：档案集合出现两次或更多次即判为损坏并返回 ErrCorruptState，错误
// 信息说明重复的是档案集合 archives 本身，而不是误报某个档案编号重复。两处
// 集合内容完全一致、只含不同编号、其中一处为空对象或 null，都按同一规则拒绝，
// 绝不合并、不取最后一份，也不按哪份保留了更多冻结挑选记录，拒绝结果与两处
// 的保存顺序无关。字段名按 JSON 字符串解码后的实际文本识别：转义写法表示同一
// 字段名也算重复；现有能识别为档案集合的大小写写法单独出现时继续可读，混用
// 它们重复保存同样失败。这条限制只针对最外层的档案集合字段：各份档案登记内容
// 中各自出现的 id、日期、冻结等同名字段是正常保存格式，不会被误判。
//
// 清册集合字段同理：同一份保存记录的最外层最多只能出现一次清册集合。普通解码
// 对同名字段只保留最后一个值，最外层写了两次 manifests 时，前一处清册集合会被
// 后一处整批替换——两份清册可能各自都符合档案归属、条目快照、期限与处理日期
// 规则（例如 APP-1 的清册收录截止日为 2025-01-10 的已销毁档案，两处处理日期
// 分别为 2025-01-10 与 2025-01-11，两个日期都满足到期规则），仅留下后一处时
// 保管库仍能打开，取回的处理日期却随保存顺序变化。已关闭清册的历史不能这样被
// 覆盖。因此解码时逐个核对最外层字段名：清册集合出现两次或更多次即判为损坏并
// 返回 ErrCorruptState，错误信息说明重复的是清册集合 manifests 本身，而不是
// 误报某个申请编号重复——即使两处分别只含不同申请，重复的也只是集合字段。两处
// 集合内容完全一致、只含不同申请、其中一处为空对象或 null，都按同一规则拒绝，
// 绝不合并两处集合、不挑选一份清册，也不重新保存来消除重复，拒绝结果与两处的
// 保存顺序无关。字段名按 JSON 字符串解码后的实际文本识别：转义写法表示同一
// 字段名也算重复；现有能识别为清册集合的大小写写法单独出现时继续可读，混用
// 它们重复保存同样失败。这条限制只针对最外层的清册集合字段：多份清册内部各自
// 带有的申请编号、处理日期和条目字段是正常保存格式，不会被误判。清册集合缺省
// 或仅一次为 null 时沿用既有空集合含义。
//
// 同一份档案自己的保存内容中，冻结列表 freezes 最多也只能出现一次：普通解码
// 对同名字段只保留最后一个值，同一份档案记录中若先保存了包含未解除诉讼冻结的
// freezes、后面又保存一个空的 freezes，读取后历史里原冻结消失，到期后的销毁
// 前核对可能误报可以办理——这样的记录不能被当成没有冻结的正常档案使用。因此
// 解码每份档案的登记记录时逐个核对字段名：冻结列表出现两次或更多次即判为损坏
// 并返回 ErrCorruptState，错误信息指出档案编号，并说明重复的是冻结列表
// freezes 本身，而不是误报某个冻结编号重复。两处列表内容完全一致、分别保存
// 不同冻结、其中一处为空列表或 null，都按同一规则拒绝，绝不合并两处列表或
// 挑选其中一份，也不重新保存来消除重复，拒绝结果与两处的保存顺序无关。字段名
// 按 JSON 字符串解码后的实际文本识别：转义后表示 freezes 的写法也算同一字段；
// 现有能识别为冻结列表的大小写写法单独出现时继续可读，混用它们重复保存同样
// 失败。检查只针对单份档案自己的保存内容，不同档案各自的冻结列表不合在一起
// 计数；冻结列表缺省、仅一次为 null 或仅有一个空列表时继续表示没有冻结，
// 合法列表里的全部冻结和解除历史仍按原顺序保留。
//
// 同一份档案自己的保存内容中，修订列表 revisions 同样最多只能出现一次：普通
// 解码对同名字段只保留最后一个值，同一份档案记录中若第一处保存着先延长后
// 缩短回最初期限的两条修订、第二处又保存一个空列表，读取后当前截止日仍与
// 最初期限一致、保管库看似能正常打开，但两次修订已经消失，已占用的修订编号
// 也可能被重新用于新业务——这样的记录不能被当成没有修订的正常档案使用。因此
// 解码每份档案的登记记录时逐个核对字段名：修订列表出现两次或更多次即判为
// 损坏并返回 ErrCorruptState，错误信息指出档案编号，并说明重复的是修订列表
// revisions 本身，而不是误报某个修订编号重复。两处列表内容完全一致、分别
// 保存不同修订、其中一处为空列表或 null，都按同一规则拒绝，绝不合并两处
// 列表或挑选其中一份，也不重新保存来消除重复，拒绝结果与两处的保存顺序
// 无关。字段名按 JSON 字符串解码后的实际文本识别：转义后表示 revisions 的
// 写法也算同一字段；现有能识别为修订列表的大小写写法单独出现时继续可读，
// 混用它们重复保存同样失败。检查只针对单份档案自己的保存内容，不同档案各自
// 的修订列表不合在一起计数；没有修订的旧档案继续缺省修订列表，单次 null 或
// 空列表继续表示没有修订，合法列表里的全部修订仍按原顺序保留，当前截止日、
// 编号占用与相同提交取回原记录的行为不变。
//
// 同一条冻结记录自己的保存内容中，解除标记 released 同样最多只能出现一次：
// 普通解码对同名字段只保留最后一个值，一条冻结若先写 released:false、后写
// released:true 并带上合法的解除原因和解除日期，读取会采用后一个标记——
// 档案到期后，销毁前核对可能显示可以办理，正式销毁也会忽略这条冻结。因此
// 解码每条冻结记录时逐个核对字段名：解除标记出现两次或更多次即判为损坏并
// 返回 ErrCorruptState，错误信息指出档案编号与冻结编号，并说明重复的是解除
// 标记 released 本身，而不是误报冻结编号或冻结列表重复。两处取值相反、完全
// 相同，或其中一处为 null，都按同一规则拒绝，绝不选取其中一个值继续使用，
// 拒绝结果与两处的保存顺序无关；即使解除原因、解除日期、档案期限和清册归属
// 均合法，也不能掩盖重复标记。字段名按 JSON 字符串解码后的实际文本识别：
// Unicode 转义后表示 released 的写法与直接写出的字段名算同一个标记；现有能
// 识别为解除标记的大小写写法单独出现时继续可读，混用后重复出现同样拒绝。
// 检查只针对一条冻结自己的保存内容，不把不同冻结或不同档案各自的解除标记
// 合在一起计算，交换字段顺序也不改变拒绝结果；解除标记缺省仍表示未解除，
// 单次 false、单次 true 且解除信息合法的正常记录继续可读。
//
// 档案编号在保存的档案集合中必须唯一：一个编号只能对应一份登记记录。
// 档案集合以 JSON 对象保存，普通解码遇到同编号键会静默保留最后一个值，
// 让后一份登记覆盖前一份（例如先保存一份带未解除诉讼冻结的 A-1，再保存
// 一份冻结列表为空的 A-1，读取后只剩后者，销毁资格会被误判为可办理）。
// 因此解码时逐键核对：同一编号出现两次即判为损坏并返回 ErrCorruptState，
// 错误信息给出重复的档案编号并说明登记重复，绝不以后一份替代前一份，也
// 不按哪份期限更长、哪份仍被冻结挑选可信记录。两份内容完全相同（类别、
// 日期、冻结与修订历史一致）也不是合并或忽略重复的理由。编号按 JSON
// 字符串解码后的实际文本识别：直接写出的 A-1 与通过 Unicode 转义写出的
// 同一编号仍算重复；不同编号各自登记一次不受影响，不同档案登记内容里
// 出现相同字段名是正常格式，不会被误判为编号重复。
//
// 清册集合中的申请编号同样必须唯一：一个已成功的销毁申请编号只能对应一份
// 已关闭清册。清册集合也以 JSON 对象保存，普通解码遇到同编号键会静默保留
// 最后一个值，让后一份清册覆盖前一份；两份清册可能各自都符合档案归属、
// 条目快照、期限与处理日期规则，仅留下后一份时保管库仍能打开，取回的清册
// 却随保存顺序改变。因此解码时逐键核对：同一申请编号对应两份清册即判为
// 损坏并返回 ErrCorruptState，错误信息给出重复的申请编号并说明该编号对应
// 多份清册，绝不以后一份替代前一份，也不按处理日期或收录档案挑选其中一份。
// 两份清册完全相同也不是合并或忽略重复的理由；两份收录相同档案但处理日期
// 不同（即使两个日期都满足到期规则）同样拒绝，拒绝结果与两份记录的保存
// 顺序无关。这种重复保存不是正常的申请重试——幂等重放应取回唯一的原清册，
// 绝不产生两份记录。编号按 JSON 字符串解码后的实际文本识别：直接写出的
// APP-1 与通过 Unicode 转义写出的同一编号仍算重复；不同申请编号各自对应
// 一份清册不受影响，不同清册记录内部带有处理日期、条目、档案编号等同名
// 字段是正常格式，不会被误判为申请编号重复。
//
// 每份档案的起算日与当前生效的保管截止日也都必须存在，且截止日不得
// 早于起算日——与登记、修订办理时的要求一致。任一档案缺少其中一个
// 日期，或两个日期的先后关系不合法，整份保管库判为损坏并返回
// ErrCorruptState，错误信息指明档案编号与缺失的日期项，顺序错误时
// 同时给出两项日期。已修订、已销毁的档案同样遵守这条当前期限规则；
// 没有修订记录的旧档案可以缺少最初截止日与修订列表（按登记截止日
// 兼容补齐最初截止日），但起算日与当前截止日不能靠兼容补齐。
//
// 冻结编号与冻结历史也必须一一对应：每份档案的全部冻结记录（含已经
// 解除的——解除只做标记，记录全部保留，原编号继续被占用）中，一个冻结
// 编号只能出现一次。同一档案保存了两条相同编号的冻结（两条都未解除、
// 一条已解除而另一条未解除，或两条均已解除；即使原因、冻结日期与解除
// 信息完全一致），该编号已无法明确对应哪次冻结，整份保管库判为损坏并
// 返回 ErrCorruptState，错误信息给出档案编号与重复的冻结编号；绝不合并
// 记录或挑选其中一条继续使用。唯一性只限定在同一份档案内，不同档案各有
// 一条同编号冻结仍是合法记录。
//
// 已销毁档案与已关闭清册也必须相互对应：每份已销毁档案记下的申请编号
// 必须能找到一份清册，且该清册恰好收录该档案一次；清册收录的每份档案
// 也必须存在、标成已销毁并指回这份清册。已销毁档案找不到清册、同一档案
// 出现在多份清册、归属指向别的申请，或未销毁档案仍挂有清册申请编号，
// 都说明记录已无法说明档案由哪次申请销毁，整份保管库判为损坏并返回
// ErrCorruptState，错误信息指明涉及的档案编号与相关申请编号。
//
// 归属对应之外，冻结状态也必须与销毁记录一致：仍被冻结的档案不能销毁，
// 已销毁档案的全部冻结都应已合法解除；只要任一已销毁档案还有一条冻结仍
// 标记为未解除，即使清册归属、条目内容和处理日期都合法，销毁记录与冻结
// 状态也互相矛盾，整份保管库判为损坏并返回 ErrCorruptState，错误信息指明
// 档案编号、未解除的冻结编号与所属清册申请编号。同一档案其他冻结已解除
// 不能抵消这一条；残留的解除日期或解除原因也不能把仍标记为未解除的冻结
// 当作已解除——是否解除只看保存的解除标记。
//
// 仅归属对应还不够：已关闭清册中的每条档案条目是销毁成功那一刻登记内容
// 的快照，其类别、起算日、截止日必须与对应档案当前保存的登记内容逐项
// 一致。档案销毁后期限不能再修订，编号、类别与起算日也从不改动，所以
// 合法记录中两处必然相同；截止日必须是销毁时最终生效的期限（最后一次
// 成功修订的新截止日，没有修订时为最初登记的截止日），即使修订历史衔接
// 完整、清册归属正确，保存成最初登记的截止日也属损坏。任一条目在类别、
// 起算日、截止日任一项上不一致，两处记录就互相矛盾，整份保管库判为
// 损坏并返回 ErrCorruptState，错误信息指明清册申请编号、档案编号与
// 不一致的项目；绝不挑选其中一处作为可信记录，也不通过覆盖清册、修改
// 档案或删除记录消除差异。一份清册收录多份档案时，一份条目矛盾就使
// 整份保管库无法读取，不会返回其余条目的正常结果。
//
// 清册的处理日期也必须满足办理销毁时同一条到期规则：每份已关闭清册都必须
// 带有处理日期，且处理日期不能早于其任一条目档案销毁时最终生效并保存到
// 清册里的截止日——截止日当天即到期，等于截止日或晚于截止日才合法。
// 缺少处理日期（字段缺失或为 null）不能当成已经办理的销毁日期；处理日期
// 早于某份档案的截止日属于提前销毁，即使该档案标记为已销毁、清册归属与
// 条目快照都一致，也说明销毁在到期前发生，两份记录无法同时成立。期限有过
// 修订的档案按清册条目保存的最终截止日判断（修订办理日期只记录办理时间，
// 不用它重新选择期限）。缺处理日期时错误指出申请编号；提前销毁时同时指出
// 申请编号、档案编号、处理日期与截止日。一份清册中只要有一份档案未到期，
// 整份保管库都判为损坏，不会只返回其余已到期档案的正常记录，也不会略过
// 未到期条目。
//
// 最初截止日、修订记录与当前截止日也必须连续对应：第一条修订的原截止日
// 等于最初截止日，后续每条的原截止日等于上一条的新截止日，最后一条的
// 新截止日等于当前截止日；没有修订时最初截止日与当前截止日相同。修订
// 日期仅用于记录，历史不按该日期重新排列。出现断开的修订关系、当前截止日
// 与末次修订不符、修订列表中存在空记录，或已有修订却缺少最初截止日时，
// 两个日期中任何一个都不能当作可靠依据，整份保管库判为损坏并返回
// ErrCorruptState，错误信息指明涉及的档案编号，能对应到具体修订时
// 同时指明修订编号。
//
// 衔接完整、当前期限合法还不够：最初截止日与每条修订的原截止日、新截止日
// 都不得早于同一档案的起算日——登记和修订办理时都不可能保存早于起算日的
// 截止日，读取保存记录时也必须核对整段期限历史。当前截止日合法、修订前后
// 衔接，中途却曾把期限缩短到起算日之前、随后又改回合法日期的历史不可能由
// 正常办理产生；最初截止日本身早于起算日、后来通过修订改到合法日期的情形
// 也一样。命中时整份保管库判为损坏并返回 ErrCorruptState，错误信息指出
// 档案编号、出错的期限位置（最初截止日或某条修订的原截止日/新截止日）、
// 该截止日与起算日，问题出在修订记录中时同时指出修订编号。已销毁档案同样
// 适用：即使已关闭清册的条目、归属和处理日期都正确，也不能掩盖历史中的
// 非法期限。绝不删除出错的修订或把历史日期改成当前截止日。
//
// 每个成功修订编号在整个保管库内只能对应一条保存的修订记录，且不能与
// 已关闭清册的申请编号相同——与办理时“修订编号全库唯一、与销毁申请编号
// 互不占用”的要求一致。同一档案历史中重复出现同一编号、不同档案各自保存
// 同号修订（即使两条记录内容完全相同），或某条修订的编号与一份已关闭
// 清册的申请编号相同，都无法仅凭编号唯一确定应取回哪条修订，整份保管库
// 判为损坏并返回 ErrCorruptState；绝不合并记录或挑选其中一条继续使用。
// 错误信息指明冲突编号：同档案重复时指出该档案，跨档案重复时指出两份
// 档案，与清册冲突时指出修订所属档案与清册申请编号。
//
// 每条已保存修订还必须保留提交修订时要求填写的办理信息：非空白的修订
// 原因与有效的修订日期齐备，历史才能说明这次调整是在何时、因何办理的。
// 保存记录中的修订原因缺失、为 null、为空串或仅含空白，都算缺少修订
// 原因；修订日期字段缺失或为 null，都算缺少修订日期（已填写的日期仍须
// 是真实的 YYYY-MM-DD 日期，否则由 JSON 解码直接判损坏）。命中任一缺项
// 都按保存记录损坏拒绝打开（ErrCorruptState），错误信息指出档案编号、
// 修订编号以及缺少的是修订原因还是修订日期，同一条修订两项同时缺少时
// 两项都说明。这项要求覆盖每一条成功修订，而不只是最后一条：先延长、
// 后缩短的历史中中间那次修订缺少原因时，不能因最终截止日合法、全部期限
// 前后衔接就接受；仍被冻结或已经销毁档案的修订历史也遵守同一要求，清册
// 的归属、条目与处理日期都正确仍不能掩盖修订信息缺项。绝不补写原因、
// 借用冻结或销毁日期，也不删除那次修订来继续使用。没有修订记录的旧档案
// 不受影响，继续沿用既有兼容行为。
func (s *Store) load() (*storeData, error) {
	raw, err := os.ReadFile(s.statePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return newStoreData(), nil
		}
		return nil, fmt.Errorf("retention: 无法读取状态文件: %w", err)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("retention: 状态文件为空或只有空白: %w", ErrCorruptState)
	}
	// json.Unmarshal 对 null 不报错且保持目标不变，必须单独拒绝。
	if string(trimmed) == "null" {
		return nil, fmt.Errorf("retention: 状态文件内容为 null，不是有效的保管库记录: %w", ErrCorruptState)
	}
	data := newStoreData()
	// json.Unmarshal 要求整个输入恰好是一个 JSON 值：
	// 合法对象后面再拼接任何内容都会在这里报错。
	if err := json.Unmarshal(raw, data); err != nil {
		// 最外层出现两次或更多次档案集合属于记录损坏而非单纯的语法错误，
		// 绝不让后一次保存的档案集合替换前一次后继续使用。
		var dupArchivesField *duplicateArchivesFieldError
		if errors.As(err, &dupArchivesField) {
			// 错误信息说明重复的是档案集合 archives 本身，不涉及具体档案编号。
			return nil, fmt.Errorf(
				"retention: 保存记录最外层的档案集合 archives 出现了多次，同一份保存记录只能包含一个档案集合，记录已损坏: %w",
				ErrCorruptState)
		}
		// 最外层出现两次或更多次清册集合同样属于记录损坏：绝不让后一次
		// 保存的清册集合整批替换前一次，使取回的已关闭清册随保存顺序变化。
		var dupManifestsField *duplicateManifestsFieldError
		if errors.As(err, &dupManifestsField) {
			// 错误信息说明重复的是清册集合 manifests 本身，不涉及具体申请编号：
			// 即使两处分别只含不同申请，重复的也只是集合字段。
			return nil, fmt.Errorf(
				"retention: 保存记录最外层的清册集合 manifests 出现了多次，同一份保存记录只能包含一个清册集合，记录已损坏: %w",
				ErrCorruptState)
		}
		// 集合中同一键出现多次属于记录损坏而非单纯的语法错误，绝不以后一份
		// 记录覆盖前一份后继续使用。
		var dupArchiveID *duplicateArchiveIDError
		if errors.As(err, &dupArchiveID) {
			// 错误信息给出重复的档案编号并说明登记重复。
			return nil, fmt.Errorf(
				"retention: 档案编号 %s 在保存的档案集合中重复登记，同一编号只能对应一份登记记录，记录已损坏: %w",
				dupArchiveID.ID, ErrCorruptState)
		}
		var dupAppID *duplicateApplicationIDError
		if errors.As(err, &dupAppID) {
			// 错误信息给出重复的申请编号并说明该编号对应多份清册：
			// 已成功的销毁申请只能对应一份已关闭清册，重复保存不是申请重试。
			return nil, fmt.Errorf(
				"retention: 申请编号 %s 在保存的清册集合中对应多份清册，一个已成功的销毁申请只能对应一份已关闭清册，记录已损坏: %w",
				dupAppID.ID, ErrCorruptState)
		}
		// 单份档案自己的保存内容中出现两次或更多次冻结列表属于记录损坏：
		// 普通解码只保留后一个列表，先保存的冻结历史会被静默替换（先保存
		// 包含未解除诉讼冻结的 freezes、后面又保存一个空的 freezes，读取后
		// 历史里原冻结消失，到期后的销毁前核对可能误报可以办理）。绝不合并
		// 或挑选其中一份列表，也不重新保存来消除重复。
		var dupFreezesField *duplicateFreezesFieldError
		if errors.As(err, &dupFreezesField) {
			// 错误信息指出档案编号，并说明重复的是冻结列表 freezes 本身，
			// 而不是误报某个冻结编号重复。
			return nil, fmt.Errorf(
				"retention: 档案 %s 的保存内容中冻结列表 freezes 出现了多次，同一份档案记录只能保存一个冻结列表，记录已损坏: %w",
				dupFreezesField.ID, ErrCorruptState)
		}
		// 单份档案自己的保存内容中出现两次或更多次修订列表同样属于记录损坏：
		// 普通解码只保留后一个列表，先保存的修订历史会被静默替换（例如第一处
		// 保存着先延长后缩短回最初期限的两条修订、第二处是空列表，读取后当前
		// 截止日仍与最初期限一致、保管库看似正常，但两次修订消失，已占用的
		// 修订编号也可能被重新用于新业务）。绝不合并或挑选其中一份列表，
		// 也不重新保存来消除重复。
		var dupRevisionsField *duplicateRevisionsFieldError
		if errors.As(err, &dupRevisionsField) {
			// 错误信息指出档案编号，并说明重复的是修订列表 revisions 本身，
			// 而不是误报某个修订编号重复。
			return nil, fmt.Errorf(
				"retention: 档案 %s 的保存内容中修订列表 revisions 出现了多次，同一份档案记录只能保存一个修订列表，记录已损坏: %w",
				dupRevisionsField.ID, ErrCorruptState)
		}
		// 同一条冻结记录的保存内容中出现两次或更多次解除标记属于记录损坏：
		// 普通解码只保留后一个标记，先保存的解除状态会被静默替换（先写
		// released:false、后写 released:true 并带上合法的解除原因和日期，
		// 读取后冻结被当成已解除，档案到期后的销毁前核对可能误报可以办理，
		// 正式销毁也会忽略这条冻结）。绝不选取其中一个值继续使用。
		var dupReleasedField *duplicateReleasedFieldError
		if errors.As(err, &dupReleasedField) {
			// 错误信息指出档案编号与冻结编号，并说明重复的是解除标记 released
			// 本身，而不是误报冻结编号或冻结列表重复。
			return nil, fmt.Errorf(
				"retention: 档案 %s 的冻结 %s 的保存内容中解除标记 released 出现了多次，同一条冻结记录只能保存一个解除标记，记录已损坏: %w",
				dupReleasedField.ArchiveID, dupReleasedField.FreezeID, ErrCorruptState)
		}
		return nil, fmt.Errorf("retention: 状态文件内容无法解析: %v: %w", err, ErrCorruptState)
	}
	if data.Archives == nil {
		data.Archives = archiveMap{}
	}
	if data.Manifests == nil {
		data.Manifests = manifestMap{}
	}
	// 档案身份必须先于一切业务校验：保存的档案集合中用于查找每份档案的编号
	// （对象键）与该份登记内容里的 id 必须同为非空白文本且逐字相同。键与 id
	// 不一致，或登记 id 缺失、为 null、空串、只有空白时，记录无法说明自己是
	// 哪份档案，后续冻结、清册等交叉引用全都失去可靠依据，必须先按整库损坏
	// 拒绝，绝不交付可继续办理的保管库。
	if err := validateArchiveIdentity(data); err != nil {
		return nil, err
	}
	// 清册身份同样必须先于一切业务校验：保存的清册集合中用于找到每份清册的
	// 编号（对象键）与该份清册内容里的 application_id 必须同为非空白文本且
	// 逐字相同。键与 application_id 不一致，或清册内容的 application_id
	// 缺失、为 null、空串、只有空白时，记录无法说明自己是哪次申请的清册，
	// 档案归属、条目快照、处理日期等交叉引用全都失去可靠依据，必须先按整库
	// 损坏拒绝，绝不交付可继续办理的保管库。
	if err := validateManifestIdentity(data); err != nil {
		return nil, err
	}
	// 冻结编号唯一性必须先于其他语义校验：同一档案的全部冻结历史中，
	// 一个冻结编号只能出现一次，解除过的记录仍占用原编号。重复时后续校验
	// 会不知道同号记录对应哪次冻结，必须先按整库损坏拒绝。
	if err := validateFreezeIDs(data); err != nil {
		return nil, err
	}
	// 每条冻结保留的原始信息必须与办理冻结时的要求一致：非空白的冻结原因
	// 与有效的冻结日期齐备。缺少任一项的冻结（无论是否已解除、所属档案
	// 是否已销毁）都不能当作正常历史使用，必须先按整库损坏拒绝。
	if err := validateFreezeOriginRecords(data); err != nil {
		return nil, err
	}
	// 语义校验先于任何兼容处理：已解除冻结必须与既有解除功能遵守同一要求——
	// 解除原因与解除日期齐备，且解除日期不早于冻结日期。
	// 只有“已解除”标记而缺少任一信息，或解除日期早于冻结日期的记录
	// 一律判为损坏（即使所属档案已销毁）；绝不据此继续办理或修补记录。
	if err := validateFreezeReleaseRecords(data); err != nil {
		return nil, err
	}
	// 每份档案的起算日与当前生效的截止日都必须存在且顺序合法，
	// 否则销毁前核对会把缺截止日的档案当成已经到期。
	if err := validateArchiveRetentionDates(data); err != nil {
		return nil, err
	}
	// 已销毁档案与已关闭清册必须相互对应，否则无法说明档案由哪次申请销毁。
	// 关系损坏时绝不挑选其中一份记录继续使用，也不补清册或改销毁标记。
	if err := validateManifestConsistency(data); err != nil {
		return nil, err
	}
	// 已销毁档案不能仍带未解除冻结：冻结挡住销毁是办理时的硬性规则，
	// 销毁后的历史也必须满足。归属、条目与处理日期即使都合法，这项矛盾
	// 仍使整库记录不可信。
	if err := validateDestroyedArchiveFreezes(data); err != nil {
		return nil, err
	}
	// 兼容引入修订功能之前保存的保管库：没有修订记录也没有最初截止日时，
	// 登记截止日就是最初截止日。已有修订却缺少最初截止日的记录不能据此
	// 冒充，由 validateRevisionContinuity 按损坏拒绝。
	for _, ar := range data.Archives {
		if ar != nil && len(ar.Revisions) == 0 && ar.InitialEnd.IsZero() {
			ar.InitialEnd = ar.End
		}
	}
	// 已关闭清册中的每条档案条目是销毁成功那一刻的登记内容快照，
	// 其类别、起算日与截止日必须与对应档案保存的登记内容逐项一致，
	// 截止日必须是该档案销毁时最终生效的期限，不能拿最初登记的截止日顶替。
	if err := validateManifestSnapshots(data); err != nil {
		return nil, err
	}
	// 已关闭清册还必须带有处理日期，且处理日期不能早于任一条目档案销毁时
	// 最终生效并保存到清册里的截止日——与办理销毁时的到期判断同一条规则。
	if err := validateManifestProcessing(data); err != nil {
		return nil, err
	}
	// 最初截止日、修订记录与当前截止日必须连续衔接，否则当前期限与
	// 历史期限相互矛盾，任何一个日期都不能当作核对依据。
	if err := validateRevisionContinuity(data); err != nil {
		return nil, err
	}
	// 最初截止日与每条修订的原截止日、新截止日都不得早于同一档案的起算日，
	// 与登记和修订办理时“截止日不能早于起算日”的要求一致：衔接完整、当前
	// 期限合法不能掩盖中途曾早于起算日的非法期限。须在兼容补齐旧档案最初
	// 截止日之后检查，覆盖最初截止日与全部历史期限。
	if err := validateRetentionHistoryDates(data); err != nil {
		return nil, err
	}
	// 成功修订编号在整个保管库内只能对应一条保存的修订记录，且不能与
	// 已关闭清册的申请编号相同——与办理时全库唯一、编号互不占用的要求一致。
	if err := validateRevisionIDs(data); err != nil {
		return nil, err
	}
	// 每条已保存修订保留的办理信息必须与提交修订时的要求一致：非空白的
	// 修订原因与有效的修订日期齐备。缺少任一项的修订（无论期限衔接是否
	// 完整、所属档案是否仍被冻结或已经销毁）都不能当作正常历史使用，必须
	// 按整库损坏拒绝。已填写但不是合法日历日期的修订日期由 JSON 解码直接
	// 判损坏。
	if err := validateRevisionOriginRecords(data); err != nil {
		return nil, err
	}
	return data, nil
}

// validateArchiveIdentity 检查保存的档案集合中，用于查找每份档案的编号
// （档案集合对象的键）与该份登记内容里的 id 是否同为非空白文本且逐字相同。
//
// 正常登记保存的记录必然满足这一条：办理登记时编号已经过去除首尾空白的
// 非空白校验，且保存时同一份编号同时用作集合键与登记内容的 id。读取保存
// 记录时却不能默认它成立：一条记录可以被挂在编号 A-1 名下，内容里的 id
// 却写成 A-2，或 id 缺失、为 null、为空串、只有空白。这样的记录无法说明
// 自己究竟是哪份档案——按 A-1 查到的历史会标着 A-2，销毁还可能把 A-2
// 写进清册，随后清册与档案对应不上，整库都无法再使用。因此这类矛盾必须
// 在进入任何业务操作前被拒绝，绝不交付一个还能继续办理的保管库。
//
// 比较以 JSON 解码后的实际文本为准：直接写出的 "A-1" 与通过 Unicode 转义
// 写出、解码后同为 A-1 的编号仍属一致；但两处仅大小写不同（A-1 与 a-1）
// 或首尾空白不同（"A-1" 与 " A-1 "）都不能当作一致——读取保存记录时绝不
// 做去首尾空白或大小写归一化。也绝不通过去空白、改号或补写缺失编号来
// “修复”保存内容：正常登记时对输入去首尾空白的行为继续保留，那只发生在
// 办理入口，不会用来改动磁盘上的既有文本。
//
// 命中时返回可由 ErrCorruptState 识别的错误：
//   - 两处编号不同：说明是档案编号不一致，并同时给出查找编号与记录内编号；
//   - 登记内容缺少有效 id（字段缺失、为 null、空串或只有空白）：指出对应
//     的查找编号与缺失位置（登记内容的 id）；
//   - 用于查找的键本身为空白：该记录没有有效的查找编号，同样按损坏拒绝。
//
// 校验覆盖整个保管库的全部档案，与档案是否尚未销毁、仍被冻结或已经销毁
// 无关：合法的期限、修订历史、冻结信息或清册归属都不能掩盖编号矛盾。
// 任一档案身份不明，整份保管库都判为损坏——load 在每次查询、核对与办理
// 前都会重新执行校验，因此保管库打开后保存内容才出现这种问题时，下一次
// 使用有效输入（即使操作的是另一份正常档案）也按整库记录损坏失败，不返回
// 正常历史、清册或部分报告，不改变档案状态或生成清册，原保存内容保持原样。
func validateArchiveIdentity(data *storeData) error {
	// map 遍历顺序不稳定，按查找编号排序后再检查，保证错误信息稳定。
	lookupIDs := make([]string, 0, len(data.Archives))
	for id := range data.Archives {
		lookupIDs = append(lookupIDs, id)
	}
	sort.Strings(lookupIDs)
	for _, lookupID := range lookupIDs {
		// 用于查找档案的编号本身也必须是非空白文本：空白键无法给记录一个
		// 有效的档案身份。打印带引号的键，空串与纯空白键都能在信息中看清。
		if strings.TrimSpace(lookupID) == "" {
			return fmt.Errorf(
				"retention: 保存的档案集合中用于查找档案的编号为空白（键 %q），该记录没有有效的查找编号，记录已损坏: %w",
				lookupID, ErrCorruptState)
		}
		ar := data.Archives[lookupID]
		if ar == nil || strings.TrimSpace(ar.ID) == "" {
			return fmt.Errorf(
				"retention: 按查找编号 %q 找到的登记记录内容里缺少有效的档案编号（id 缺失、为 null、为空串或只有空白），无法确认该记录的档案身份，记录已损坏: %w",
				lookupID, ErrCorruptState)
		}
		// 逐字比较解码后的文本，不做去空白或大小写归一化：
		// 首尾空白或大小写不同也算两处编号不一致。
		if ar.ID != lookupID {
			return fmt.Errorf(
				"retention: 按查找编号 %q 找到的登记记录，其内容里的档案编号 id 为 %q，两处编号不一致，记录已损坏: %w",
				lookupID, ar.ID, ErrCorruptState)
		}
	}
	return nil
}

// validateManifestIdentity 检查保存的清册集合中，用于找到每份已关闭清册的
// 编号（清册集合对象的键）与该份清册内容里的 application_id 是否同为非空白
// 文本且逐字相同。
//
// 正常办理销毁保存的记录必然满足这一条：申请编号在办理入口已经过去除首尾
// 空白的非空白校验，保存时同一个编号同时用作清册集合键与清册内容的
// application_id。读取保存记录时却不能默认它成立：一份清册可以被挂在申请
// APP-1 名下，内容里的 application_id 却写成 APP-2，或 application_id 缺失、
// 为 null、为空串、只有空白。这样的记录无法说明自己是哪次申请的清册——
// 按 APP-1 取回的清册标着 APP-2，重提原申请也会拿到编号不符的清册，随后
// 清册与档案的归属关系也失去可靠依据。因此这类矛盾必须在进入任何业务
// 操作前被拒绝，绝不交付一个还能继续办理的保管库。
//
// 比较以 JSON 解码后的实际文本为准：直接写出的 "APP-1" 与通过 Unicode
// 转义写出、解码后同为 APP-1 的编号仍属一致；但两处仅大小写不同
// （APP-1 与 app-1）或首尾空白不同（"APP-1" 与 " APP-1 "）都不能当作
// 一致——读取保存记录时绝不做去首尾空白或大小写归一化。也绝不通过用查找
// 编号补写清册、以清册内容改号或删除冲突记录来“修复”保存内容：正常提交
// 时对申请编号去首尾空白的行为继续保留，那只发生在办理入口，不会用来改动
// 磁盘上的既有文本。
//
// 命中时返回可由 ErrCorruptState 识别的错误：
//   - 用于查找的键本身为空白：说明该清册记录没有有效的查找编号；
//   - 清册内容缺少有效 application_id（字段缺失、为 null、空串或只有空白）：
//     指出对应的查找编号与缺失位置（清册内容的 application_id）；
//   - 两处编号不同：说明是申请编号不一致，并同时给出查找编号与清册内容里
//     的申请编号。
//
// 校验覆盖整个保管库的全部清册，与条目快照、处理日期、档案归属或期限修订
// 是否合法无关：即使档案已经销毁、归属关系正确、期限修订完整、其他校验
// 全部通过，合法清册的其他内容也不能掩盖编号矛盾。任一清册身份不明，整份
// 保管库都判为损坏——load 在每次查询、核对与办理前都会重新执行校验，因此
// 保管库打开后保存内容才出现这种问题时，下一次使用有效输入（即使查询、
// 核对或办理的是另一份正常档案、另一份正常清册）也按整库记录损坏失败，
// 不返回正常历史、清册或部分报告，不写入业务变更，原保存内容保持原样。
func validateManifestIdentity(data *storeData) error {
	// map 遍历顺序不稳定，按查找编号排序后再检查，保证错误信息稳定。
	lookupIDs := make([]string, 0, len(data.Manifests))
	for id := range data.Manifests {
		lookupIDs = append(lookupIDs, id)
	}
	sort.Strings(lookupIDs)
	for _, lookupID := range lookupIDs {
		// 用于查找清册的编号本身也必须是非空白文本：空白键无法给记录一个
		// 有效的申请身份。打印带引号的键，空串与纯空白键都能在信息中看清。
		if strings.TrimSpace(lookupID) == "" {
			return fmt.Errorf(
				"retention: 保存的清册集合中用于查找清册的申请编号为空白（键 %q），该清册记录没有有效的查找编号，记录已损坏: %w",
				lookupID, ErrCorruptState)
		}
		mr := data.Manifests[lookupID]
		if mr == nil || strings.TrimSpace(mr.ApplicationID) == "" {
			return fmt.Errorf(
				"retention: 按查找编号 %q 找到的清册记录内容里缺少有效的申请编号（application_id 缺失、为 null、为空串或只有空白），无法确认该清册属于哪次申请，记录已损坏: %w",
				lookupID, ErrCorruptState)
		}
		// 逐字比较解码后的文本，不做去空白或大小写归一化：
		// 首尾空白或大小写不同也算两处编号不一致。
		if mr.ApplicationID != lookupID {
			return fmt.Errorf(
				"retention: 按查找编号 %q 找到的清册，其内容里的申请编号 application_id 为 %q，两处编号不一致，记录已损坏: %w",
				lookupID, mr.ApplicationID, ErrCorruptState)
		}
	}
	return nil
}

// validateFreezeIDs 检查每份档案的全部冻结历史中冻结编号是否唯一。
//
// 与办理新增冻结时“冻结编号同档案内唯一”的要求一致，读取保存记录时也必须
// 守住这条规则：同一份档案的全部冻结记录（含已经解除的——解除只做标记，
// 记录全部保留，原编号继续占用）中，一个冻结编号只能出现一次。同一档案保存
// 了两条相同编号的冻结时（两条都未解除、一条已解除而另一条未解除，或两条均
// 已解除；即使原因、冻结日期与解除信息完全一致），该编号已无法明确对应哪次
// 冻结，也无法确定解除应落在哪一条上，返回可由 ErrCorruptState 识别的错误，
// 并在信息中给出档案编号与重复的冻结编号：绝不合并成一条，也不挑选其中一条
// 继续使用。
//
// 唯一性只限定在同一份档案内：不同档案各有一条同编号冻结仍是合法记录，解除
// 其中一份档案的冻结不影响另一份。校验覆盖整个保管库的全部档案（含已销毁
// 的），与本次办理名单或查询目标无关：一份档案的冻结历史出现重复就使整份
// 保管库无法读取，即使本次查看或操作的是另一份正常档案，也不会返回其余正常
// 档案的记录。绝不通过改冻结编号、删除、合并或自动解除重复记录来消除问题。
// 冻结编号为空白同样不能作为已登记记录存在，一并按损坏处理。
func validateFreezeIDs(data *storeData) error {
	// map 遍历顺序不稳定，按档案编号排序、冻结按登记顺序检查，
	// 保证错误信息稳定。
	archiveIDs := make([]string, 0, len(data.Archives))
	for id := range data.Archives {
		archiveIDs = append(archiveIDs, id)
	}
	sort.Strings(archiveIDs)
	for _, id := range archiveIDs {
		ar := data.Archives[id]
		if ar == nil {
			return fmt.Errorf("retention: 档案 %s 的登记记录缺失，状态文件已损坏: %w",
				id, ErrCorruptState)
		}
		seen := make(map[string]struct{}, len(ar.Freezes))
		for _, fr := range ar.Freezes {
			if fr == nil {
				return fmt.Errorf("retention: 档案 %s 下存在缺失的冻结记录，状态文件已损坏: %w",
					id, ErrCorruptState)
			}
			if strings.TrimSpace(fr.ID) == "" {
				return fmt.Errorf(
					"retention: 档案 %s 的冻结历史中存在没有编号的冻结记录，记录已损坏: %w",
					id, ErrCorruptState)
			}
			if _, dup := seen[fr.ID]; dup {
				return fmt.Errorf(
					"retention: 档案 %s 的冻结历史中冻结编号 %s 出现多条记录，同一档案的冻结编号必须唯一，记录已损坏: %w",
					id, fr.ID, ErrCorruptState)
			}
			seen[fr.ID] = struct{}{}
		}
	}
	return nil
}

// validateFreezeOriginRecords 检查库内每条冻结保留的原始信息是否完整：
// 非空白的冻结原因与有效的冻结日期必须齐备。
//
// 办理新增冻结时，冻结原因（去除首尾空白后不得为空白）与冻结日期（真实的
// YYYY-MM-DD 日期）都是必填项，保存下来的每条冻结也必须满足同一要求。
// 保存记录中的冻结原因缺失、为 null、为空串或仅含空白，都算缺少冻结原因；
// 冻结日期字段缺失或为 null，都算缺少冻结日期（已填写的日期仍由 Date 的
// 解析校验守住真实日期要求）。缺少任一项的冻结已无法说明自己因何、自何时
// 冻结，不能当作正常记录交给历史查询与销毁前核对：返回可由 ErrCorruptState
// 识别的错误，错误信息给出档案编号、冻结编号，并说明缺少的是冻结原因还是
// 冻结日期；两项同时缺少时两项都说明。
//
// 校验覆盖整个保管库全部档案的全部冻结历史——未解除与已解除的冻结都检查，
// 已销毁档案的冻结同样检查：已解除冻结保存的解除日期与解除原因再完整，也
// 不能拿解除日期代替冻结日期、拿解除原因补齐冻结原因；清册归属、条目与
// 处理日期都正确也不能掩盖这条缺项。绝不自动补写日期或原因、删除冻结或
// 更改解除状态，原保存内容保持原样。没有冻结的档案是合法记录，不在此报错。
func validateFreezeOriginRecords(data *storeData) error {
	// map 遍历顺序不稳定，按档案编号排序、冻结按登记顺序检查，
	// 保证错误信息稳定。
	archiveIDs := make([]string, 0, len(data.Archives))
	for id := range data.Archives {
		archiveIDs = append(archiveIDs, id)
	}
	sort.Strings(archiveIDs)
	for _, id := range archiveIDs {
		ar := data.Archives[id]
		if ar == nil {
			// 缺失的登记记录已由 validateFreezeIDs 报告。
			continue
		}
		for _, fr := range ar.Freezes {
			if fr == nil {
				// 空冻结记录已由 validateFreezeIDs 报告。
				continue
			}
			reasonMissing := strings.TrimSpace(fr.Reason) == ""
			dateMissing := fr.FrozenOn == nil || fr.FrozenOn.IsZero()
			switch {
			case reasonMissing && dateMissing:
				return fmt.Errorf(
					"retention: 档案 %s 的冻结 %s 缺少冻结原因与冻结日期，记录已损坏: %w",
					id, fr.ID, ErrCorruptState)
			case reasonMissing:
				return fmt.Errorf(
					"retention: 档案 %s 的冻结 %s 缺少冻结原因，记录已损坏: %w",
					id, fr.ID, ErrCorruptState)
			case dateMissing:
				return fmt.Errorf(
					"retention: 档案 %s 的冻结 %s 缺少冻结日期，记录已损坏: %w",
					id, fr.ID, ErrCorruptState)
			}
		}
	}
	return nil
}

// validateFreezeReleaseRecords 检查库内全部已解除冻结记录是否完整合法。
//
// 正常解除保存的记录必然同时带有非空白的解除原因和有效的解除日期，
// 且解除日期不早于冻结日期。缺少解除原因、解除日期缺失或为 null、
// 解除日期早于冻结日期，都说明保存内容已损坏，返回可由 ErrCorruptState
// 识别的错误，并在信息中给出涉及的档案编号与冻结编号，便于定位记录。
// 校验覆盖整个保管库的全部已解除冻结，与本次办理名单无关：
// 即使异常记录所属档案已经销毁，也不能把不完整的解除信息当作正常历史。
// 未解除冻结没有解除日期和原因是合法状态，不在此报错。
// 调用前 validateFreezeOriginRecords 已确认每条冻结的冻结原因与冻结日期
// 齐备，这里的日期比较必然有有效的冻结日期可依。
func validateFreezeReleaseRecords(data *storeData) error {
	// map 遍历顺序不稳定，按档案编号排序后再检查，保证错误信息稳定。
	archiveIDs := make([]string, 0, len(data.Archives))
	for id := range data.Archives {
		archiveIDs = append(archiveIDs, id)
	}
	sort.Strings(archiveIDs)
	for _, id := range archiveIDs {
		ar := data.Archives[id]
		if ar == nil {
			return fmt.Errorf("retention: 档案 %s 的登记记录缺失，状态文件已损坏: %w",
				id, ErrCorruptState)
		}
		for _, fr := range ar.Freezes {
			if fr == nil {
				return fmt.Errorf("retention: 档案 %s 下存在缺失的冻结记录，状态文件已损坏: %w",
					id, ErrCorruptState)
			}
			if !fr.Released {
				// 未解除冻结没有解除日期与原因是合法状态。
				continue
			}
			switch {
			case strings.TrimSpace(fr.ReleaseReason) == "":
				return fmt.Errorf(
					"retention: 档案 %s 的冻结 %s 标记为已解除但缺少解除原因，记录已损坏: %w",
					id, fr.ID, ErrCorruptState)
			case fr.ReleasedOn == nil:
				return fmt.Errorf(
					"retention: 档案 %s 的冻结 %s 标记为已解除但缺少解除日期，记录已损坏: %w",
					id, fr.ID, ErrCorruptState)
			case fr.ReleasedOn.IsZero():
				return fmt.Errorf(
					"retention: 档案 %s 的冻结 %s 的解除日期无效，记录已损坏: %w",
					id, fr.ID, ErrCorruptState)
			case fr.ReleasedOn.Before(*fr.FrozenOn):
				return fmt.Errorf(
					"retention: 档案 %s 的冻结 %s 的解除日期 %s 早于冻结日期 %s，记录已损坏: %w",
					id, fr.ID, fr.ReleasedOn, *fr.FrozenOn, ErrCorruptState)
			}
		}
	}
	return nil
}

// validateArchiveRetentionDates 检查库内每份档案的起算日与当前生效的保管截止日
// 是否齐备且顺序合法。
//
// 登记与修订在办理时都要求起算日、截止日齐备且截止日不早于起算日，保存下来
// 的记录也必须满足同一条规则：任一档案缺少起算日或当前截止日（字段缺失、
// 为 null 或解析后为零值），或两个日期齐备但截止日早于起算日，都说明保存
// 内容已损坏，返回可由 ErrCorruptState 识别的错误。缺少日期时错误信息指出
// 档案编号并说明缺的是起算日还是截止日（两者都缺则一并指出）；顺序错误时
// 同时给出起算日与截止日两项日期，便于定位记录。
//
// 没有这条校验，缺少当前截止日的档案在销毁前核对中会被当成已经到期
// （任何处理日期都不早于零值截止日），正式提交也可能据此生成清册——
// 一份无法确认保管期限的档案绝不能获得可以销毁的结论。
//
// 校验覆盖整个保管库的全部档案，与本次办理名单或查询目标无关：已修订的档案
// 按当前生效的截止日检查（修订历史衔接完整不能替代当前期限的存在），已销毁
// 的档案同样检查（已有清册不能略过这条规则）；没有修订记录的旧档案可以缺少
// 最初截止日与修订列表（由 load 中的兼容处理按登记截止日补齐最初截止日），
// 但起算日与当前截止日本身不能靠兼容补齐。截止日等于起算日是合法记录，
// 按截止日当天核对即到期，不在此报错。
func validateArchiveRetentionDates(data *storeData) error {
	// map 遍历顺序不稳定，按档案编号排序后再检查，保证错误信息稳定。
	archiveIDs := make([]string, 0, len(data.Archives))
	for id := range data.Archives {
		archiveIDs = append(archiveIDs, id)
	}
	sort.Strings(archiveIDs)
	for _, id := range archiveIDs {
		ar := data.Archives[id]
		if ar == nil {
			// 缺失的登记记录已由 validateFreezeReleaseRecords 报告。
			continue
		}
		switch {
		case ar.Start.IsZero() && ar.End.IsZero():
			return fmt.Errorf(
				"retention: 档案 %s 缺少起算日与当前保管截止日，记录已损坏: %w",
				id, ErrCorruptState)
		case ar.Start.IsZero():
			return fmt.Errorf(
				"retention: 档案 %s 缺少起算日，记录已损坏: %w",
				id, ErrCorruptState)
		case ar.End.IsZero():
			return fmt.Errorf(
				"retention: 档案 %s 缺少当前保管截止日，记录已损坏: %w",
				id, ErrCorruptState)
		case ar.End.Before(ar.Start):
			return fmt.Errorf(
				"retention: 档案 %s 的当前保管截止日 %s 早于起算日 %s，记录已损坏: %w",
				id, ar.End, ar.Start, ErrCorruptState)
		}
	}
	return nil
}

// validateManifestConsistency 检查已销毁档案与已关闭清册之间的对应关系。
//
// 正常销毁保存的记录必然满足双向对应：每份已销毁档案记下的申请编号
// 能找到一份清册，且该清册恰好收录该档案一次；清册收录的每份档案都
// 存在、标成已销毁，并指回这份清册。已销毁档案没有清册归属、归属的
// 清册不存在或未收录该档案、同一档案出现在多份清册、清册重复收录同一
// 档案、清册收录了不存在或未销毁的档案、档案归属指向别的申请，以及
// 未销毁档案仍挂有清册申请编号，都说明保存内容已损坏，返回可由
// ErrCorruptState 识别的错误，并在信息中给出涉及的档案编号与相关
// 申请编号，便于定位记录。校验覆盖整个保管库，与本次办理名单无关；
// 未销毁且没有清册归属的档案是合法记录，不在此报错。
func validateManifestConsistency(data *storeData) error {
	// map 遍历顺序不稳定，按编号排序后再检查，保证错误信息稳定。
	archiveIDs := make([]string, 0, len(data.Archives))
	for id := range data.Archives {
		archiveIDs = append(archiveIDs, id)
	}
	sort.Strings(archiveIDs)
	for _, id := range archiveIDs {
		ar := data.Archives[id]
		if ar == nil {
			// 缺失的登记记录已由 validateFreezeReleaseRecords 报告。
			continue
		}
		if !ar.Destroyed {
			if ar.ManifestID != "" {
				return fmt.Errorf(
					"retention: 档案 %s 未销毁却挂有清册申请编号 %s，记录已损坏: %w",
					id, ar.ManifestID, ErrCorruptState)
			}
			continue
		}
		if ar.ManifestID == "" {
			return fmt.Errorf(
				"retention: 档案 %s 已销毁但没有记录所属清册的申请编号，记录已损坏: %w",
				id, ErrCorruptState)
		}
		rec, ok := data.Manifests[ar.ManifestID]
		if !ok || rec == nil {
			return fmt.Errorf(
				"retention: 档案 %s 已销毁，但其所属清册 %s 不存在，记录已损坏: %w",
				id, ar.ManifestID, ErrCorruptState)
		}
		count := 0
		for _, e := range rec.Entries {
			if e.ID == id {
				count++
			}
		}
		if count != 1 {
			return fmt.Errorf(
				"retention: 档案 %s 在其所属清册 %s 中收录 %d 次（应恰好一次），记录已损坏: %w",
				id, ar.ManifestID, count, ErrCorruptState)
		}
	}

	applicationIDs := make([]string, 0, len(data.Manifests))
	for id := range data.Manifests {
		applicationIDs = append(applicationIDs, id)
	}
	sort.Strings(applicationIDs)
	for _, appID := range applicationIDs {
		rec := data.Manifests[appID]
		if rec == nil {
			return fmt.Errorf(
				"retention: 清册 %s 的内容缺失，记录已损坏: %w",
				appID, ErrCorruptState)
		}
		seen := make(map[string]int, len(rec.Entries))
		for _, e := range rec.Entries {
			seen[e.ID]++
			ar, ok := data.Archives[e.ID]
			if !ok || ar == nil {
				return fmt.Errorf(
					"retention: 清册 %s 收录的档案 %s 不存在，记录已损坏: %w",
					appID, e.ID, ErrCorruptState)
			}
			if !ar.Destroyed {
				return fmt.Errorf(
					"retention: 清册 %s 收录的档案 %s 未标记为已销毁，记录已损坏: %w",
					appID, e.ID, ErrCorruptState)
			}
			if ar.ManifestID != appID {
				return fmt.Errorf(
					"retention: 清册 %s 收录的档案 %s 归属另一申请 %s，记录已损坏: %w",
					appID, e.ID, ar.ManifestID, ErrCorruptState)
			}
		}
		// 同一档案被同一份清册重复收录（档案侧的恰好一次检查只覆盖
		// 归属指向该清册的情况，这里对全部收录记录再核对一遍）。
		entryIDs := make([]string, 0, len(seen))
		for id := range seen {
			entryIDs = append(entryIDs, id)
		}
		sort.Strings(entryIDs)
		for _, id := range entryIDs {
			if seen[id] > 1 {
				return fmt.Errorf(
					"retention: 清册 %s 重复收录档案 %s（共 %d 次），记录已损坏: %w",
					appID, id, seen[id], ErrCorruptState)
			}
		}
	}
	return nil
}

// validateDestroyedArchiveFreezes 检查已销毁档案是否仍带着未解除的冻结。
//
// 办理销毁时，任一未解除冻结都会阻止销毁（见 evaluateArchive），因此保存
// 下来的历史也必须满足同一条规则：每份已销毁档案的全部冻结都应已合法解除。
// 档案已标记为已销毁、所属清册存在且清册归属、条目快照与处理日期都合法，
// 但该档案仍有一条冻结标记为未解除（released 为 false）时，销毁记录与冻结
// 状态互相矛盾，两份记录无法同时成立，整份保管库判为损坏并返回
// ErrCorruptState，错误信息给出档案编号、未解除的冻结编号与所属清册的申请
// 编号，让调用者知道是哪份销毁记录与冻结状态冲突。
//
// 同一档案有多条冻结时，其他冻结已经解除不能抵消这一条阻碍，逐份档案只要
// 命中第一条未解除冻结即报错；即使这条记录里残留了解除日期或解除原因，只要
// 仍标记为未解除，就不能据此当作已解除——是否解除只沿用保存的解除标记判断，
// 解除日期与原因的合法性仍由 validateFreezeReleaseRecords 单独核对。
// 校验覆盖整个保管库的全部已销毁档案，与本次办理名单或查询目标无关：
// 一份清册收录多份档案，只有其中一份矛盾，或调用者只操作另一份正常档案时，
// 整份保管库同样判为损坏，绝不返回其余档案的正常结果。绝不通过自动解除冻结、
// 删除冻结历史、修改销毁标记或重建清册来消除冲突。
// 尚未销毁的档案保留未解除冻结是合法状态，不在此报错。
func validateDestroyedArchiveFreezes(data *storeData) error {
	// map 遍历顺序不稳定，按档案编号排序、冻结按登记顺序检查，
	// 保证错误信息稳定。
	archiveIDs := make([]string, 0, len(data.Archives))
	for id := range data.Archives {
		archiveIDs = append(archiveIDs, id)
	}
	sort.Strings(archiveIDs)
	for _, id := range archiveIDs {
		ar := data.Archives[id]
		if ar == nil || !ar.Destroyed {
			// 缺失的登记记录已由 validateFreezeReleaseRecords 报告；
			// 尚未销毁的档案保留未解除冻结是合法状态。
			continue
		}
		for _, fr := range ar.Freezes {
			if fr == nil {
				// 空冻结记录已由 validateFreezeReleaseRecords 报告。
				continue
			}
			if !fr.Released {
				// 归属关系已由 validateManifestConsistency 确认：
				// 已销毁档案必然带有存在的所属清册申请编号。
				return fmt.Errorf(
					"retention: 档案 %s 已销毁（所属清册 %s），但冻结 %s 仍标记为未解除，冻结状态与销毁记录冲突，记录已损坏: %w",
					id, ar.ManifestID, fr.ID, ErrCorruptState)
			}
		}
	}
	return nil
}

// validateManifestSnapshots 检查每份已关闭清册中的档案条目是否与对应档案
// 当前保存的登记内容逐项一致。
//
// 清册在销毁成功那一刻关闭，条目是关闭瞬间登记内容的快照：档案销毁后
// 不能再修订期限，编号、类别与起算日也从不改动，因此合法记录中清册条目
// 的类别、起算日与截止日必然与对应档案保存的值相同——其中截止日必须是
// 该档案销毁时最终生效的期限（等于最后一次成功修订的新截止日；没有修订
// 时等于最初登记的截止日），不能拿最初登记的截止日顶替。延长、缩短或
// 改回曾经使用过的日期都沿用这一规则，不按修订记录中的办理日期重新选择。
//
// 任一条目在类别、起算日、截止日任一项上与档案不一致，两处记录就互相
// 矛盾，任何一处都不能当作可信依据：返回可由 ErrCorruptState 识别的错误，
// 绝不挑选其中一份继续使用，也不通过覆盖清册、修改档案或删除记录消除差异。
// 错误信息给出清册申请编号、档案编号与不一致的项目（类别、起算日、
// 保管截止日，并附上两处各自保存的值）。校验覆盖整个保管库的全部清册
// 与全部条目，与本次办理名单或查询目标无关：一份清册收录多份档案时，
// 只要有一份条目矛盾，整份保管库都判为损坏，不会返回其余条目的正常结果。
func validateManifestSnapshots(data *storeData) error {
	// map 遍历顺序不稳定，按申请编号、条目顺序检查，保证错误信息稳定。
	appIDs := make([]string, 0, len(data.Manifests))
	for appID := range data.Manifests {
		appIDs = append(appIDs, appID)
	}
	sort.Strings(appIDs)
	for _, appID := range appIDs {
		rec := data.Manifests[appID]
		if rec == nil {
			// 缺失的清册记录已由 validateManifestConsistency 报告。
			continue
		}
		for _, e := range rec.Entries {
			ar, ok := data.Archives[e.ID]
			if !ok || ar == nil {
				// 清册收录不存在档案等归属问题已由 validateManifestConsistency 报告。
				continue
			}
			items := make([]string, 0, 3)
			if e.Category != ar.Category {
				items = append(items, fmt.Sprintf("类别（清册 %q，档案 %q）", e.Category, ar.Category))
			}
			if !e.Start.Equal(ar.Start) {
				items = append(items, fmt.Sprintf("起算日（清册 %s，档案 %s）", e.Start, ar.Start))
			}
			if !e.End.Equal(ar.End) {
				items = append(items, fmt.Sprintf("保管截止日（清册 %s，档案 %s）", e.End, ar.End))
			}
			if len(items) > 0 {
				return fmt.Errorf(
					"retention: 清册 %s 中档案 %s 的条目与档案登记内容不一致：%s，记录已损坏: %w",
					appID, e.ID, strings.Join(items, "、"), ErrCorruptState)
			}
		}
	}
	return nil
}

// validateManifestProcessing 检查每份已关闭清册是否带有处理日期，以及处理日期
// 是否满足办理销毁时同一条到期规则。
//
// 办理销毁时，只有处理日期不早于名单中任一份档案当时生效的保管截止日才会成功
// （截止日当天即到期，见 evaluateArchive），清册条目保存的截止日正是销毁时
// 最终生效的期限（已由 validateManifestSnapshots 确认与档案当前登记一致）。
// 因此保存的清册还必须满足：
//   - 处理日期必须存在：清册缺少处理日期或保存为 null 时，不能把缺失日期当成
//     已经办理的销毁日期；
//   - 处理日期不能早于任一条目保存的截止日：等于截止日（当天）或晚于截止日
//     才合法。期限有过修订的档案按销毁时最终生效并保存到清册里的截止日判断，
//     延长、缩短或改回早先用过的日期都一样，绝不按修订办理日期重新挑选另一版期限。
//
// 缺少处理日期时返回可由 ErrCorruptState 识别的错误并指出清册申请编号；
// 处理日期早于某条档案的截止日时同样判为损坏，错误同时指出申请编号、档案编号、
// 处理日期与该档案的截止日。一份清册收录多份档案时，只要有一份尚未到期
// （例如处理日期等于第一份的截止日、却早于第二份的截止日），整份保管库都
// 判为损坏：不会只返回已到期条目的正常记录，也不会把未到期条目略过。校验
// 覆盖整个保管库的全部清册，与本次办理名单或查询目标无关；绝不通过改动档案
// 或清册（补处理日期、改期限、改销毁标记或条目）消除矛盾。
func validateManifestProcessing(data *storeData) error {
	// map 遍历顺序不稳定，按申请编号排序、条目按清册内保存顺序检查，
	// 保证错误信息稳定。
	appIDs := make([]string, 0, len(data.Manifests))
	for appID := range data.Manifests {
		appIDs = append(appIDs, appID)
	}
	sort.Strings(appIDs)
	for _, appID := range appIDs {
		rec := data.Manifests[appID]
		if rec == nil {
			// 缺失的清册记录已由 validateManifestConsistency 报告。
			continue
		}
		if rec.ProcessedOn == nil || rec.ProcessedOn.IsZero() {
			return fmt.Errorf(
				"retention: 清册 %s 缺少处理日期，不能视为已经办理的销毁清册，记录已损坏: %w",
				appID, ErrCorruptState)
		}
		processedOn := *rec.ProcessedOn
		for _, e := range rec.Entries {
			ar, ok := data.Archives[e.ID]
			if !ok || ar == nil {
				// 清册收录不存在档案等归属问题已由 validateManifestConsistency 报告。
				continue
			}
			// 条目截止日已由 validateManifestSnapshots 确认与档案最终生效的
			// 截止日一致，直接按保存在清册里的截止日判断到期。
			if processedOn.Before(e.End) {
				return fmt.Errorf(
					"retention: 清册 %s 的处理日期 %s 早于档案 %s 的保管截止日 %s（截止日当天才算到期），属于提前销毁，记录已损坏: %w",
					appID, processedOn, e.ID, e.End, ErrCorruptState)
			}
		}
	}
	return nil
}

// validateRevisionContinuity 检查每份档案的最初截止日、修订记录与当前截止日
// 是否连续对应。
//
// 正常办理保存的记录必然满足：第一条修订的原截止日等于最初截止日，后续每条
// 的原截止日等于上一条的新截止日，最后一条的新截止日等于当前截止日；没有
// 修订时最初截止日与当前截止日相同。修订日期仅用于记录办理时间，不决定生效
// 先后，因此历史不按修订日期重新排列，只按保存顺序核对衔接；期限被延长、
// 缩短或改回早先用过的日期，只要衔接完整都是合法记录。
//
// 出现断开的修订关系、当前截止日与末次修订的新截止日不符、修订列表中存在
// 空记录，或已有修订却缺少最初截止日（不能把当前期限冒充最初期限）时，
// 保存内容已无法说明期限如何演变，返回可由 ErrCorruptState 识别的错误，
// 并在信息中给出涉及的档案编号；能对应到具体修订时同时给出修订编号。
// 校验覆盖整个保管库的全部档案（含已销毁的），与本次办理名单无关。
func validateRevisionContinuity(data *storeData) error {
	// map 遍历顺序不稳定，按档案编号排序后再检查，保证错误信息稳定。
	archiveIDs := make([]string, 0, len(data.Archives))
	for id := range data.Archives {
		archiveIDs = append(archiveIDs, id)
	}
	sort.Strings(archiveIDs)
	for _, id := range archiveIDs {
		ar := data.Archives[id]
		if ar == nil {
			// 缺失的登记记录已由 validateFreezeReleaseRecords 报告。
			continue
		}
		if len(ar.Revisions) == 0 {
			// 没有修订时最初截止日与当前截止日必须相同
			// （旧记录缺少最初截止日的情形已在 load 中按登记截止日补齐）。
			if !ar.InitialEnd.Equal(ar.End) {
				return fmt.Errorf(
					"retention: 档案 %s 没有修订记录，但最初截止日 %s 与当前截止日 %s 不一致，记录已损坏: %w",
					id, ar.InitialEnd, ar.End, ErrCorruptState)
			}
			continue
		}
		for i, rec := range ar.Revisions {
			if rec == nil {
				return fmt.Errorf(
					"retention: 档案 %s 的修订列表第 %d 条为空记录，记录已损坏: %w",
					id, i+1, ErrCorruptState)
			}
		}
		if ar.InitialEnd.IsZero() {
			return fmt.Errorf(
				"retention: 档案 %s 已有修订记录（首条为 %s）却缺少最初截止日，记录已损坏: %w",
				id, ar.Revisions[0].ID, ErrCorruptState)
		}
		// 按保存顺序逐条核对衔接：每条的原截止日必须等于此前生效的截止日。
		expected := ar.InitialEnd
		for _, rec := range ar.Revisions {
			if !rec.OldEnd.Equal(expected) {
				return fmt.Errorf(
					"retention: 档案 %s 的修订 %s 的原截止日 %s 与此前生效的截止日 %s 不衔接，记录已损坏: %w",
					id, rec.ID, rec.OldEnd, expected, ErrCorruptState)
			}
			expected = rec.NewEnd
		}
		if !ar.End.Equal(expected) {
			return fmt.Errorf(
				"retention: 档案 %s 的当前截止日 %s 与末次修订 %s 的新截止日 %s 不符，记录已损坏: %w",
				id, ar.End, ar.Revisions[len(ar.Revisions)-1].ID, expected, ErrCorruptState)
		}
	}
	return nil
}

// validateRetentionHistoryDates 检查每份档案的最初截止日以及每条修订中的
// 原截止日、新截止日是否都不早于该档案的起算日。
//
// 登记与修订办理时都硬性要求截止日不早于起算日，且修订只能在合法期限之间
// 衔接，因此正常办理产生的历史中，最初截止日与每一条修订的原截止日、新
// 截止日都不可能早于同一档案的起算日。读取保存记录时只核对当前期限合法、
// 修订前后衔接仍有缺口：例如起算日 2020-01-01、最初截止日 2025-01-10，
// 第一条修订把期限缩短到 2019-12-31、第二条再延长到 2026-01-10，两条修订
// 完全衔接且当前截止日等于末次修订的新值，但中途的 2019-12-31 不可能由
// 正常办理产生——即使最后改回合法日期也不能接受。最初截止日本身早于
// 起算日、后来通过修订改到合法日期的情形同样如此。
//
// 命中非法期限时返回可由 ErrCorruptState 识别的错误，错误信息指出档案编号、
// 出错的期限位置（最初截止日，或某条修订的原截止日/新截止日）、该截止日与
// 起算日；问题发生在修订记录中时同时指出修订编号，便于定位记录。截止日
// 等于起算日是合法记录（截止日当天即到期），不在此报错；合法的延长、缩短
// 与改回早先用过的日期都不受影响。校验覆盖整个保管库的全部档案（含已
// 销毁的），与本次办理名单或查询目标无关：即使已关闭清册的条目、归属和
// 处理日期都正确，也不能掩盖历史中的非法期限。绝不删除出错的修订，也不
// 把历史日期改成当前截止日，原保存内容保持原样。
//
// 调用前 validateArchiveRetentionDates 已确认起算日与当前截止日齐备且当前
// 截止日不早于起算日，validateRevisionContinuity 已确认修订链衔接且不存在
// 空记录，旧档案缺少的最初截止日也已在 load 中按登记截止日补齐——因此这里
// 检查的日期都必然存在，只需比较先后顺序。
func validateRetentionHistoryDates(data *storeData) error {
	// map 遍历顺序不稳定，按档案编号排序、修订按成功办理顺序检查，
	// 保证错误信息稳定。
	archiveIDs := make([]string, 0, len(data.Archives))
	for id := range data.Archives {
		archiveIDs = append(archiveIDs, id)
	}
	sort.Strings(archiveIDs)
	for _, id := range archiveIDs {
		ar := data.Archives[id]
		if ar == nil {
			// 缺失的登记记录已由 validateFreezeReleaseRecords 报告。
			continue
		}
		if ar.InitialEnd.Before(ar.Start) {
			return fmt.Errorf(
				"retention: 档案 %s 的最初截止日 %s 早于起算日 %s，保存的期限历史不可能由正常办理产生，记录已损坏: %w",
				id, ar.InitialEnd, ar.Start, ErrCorruptState)
		}
		for _, rec := range ar.Revisions {
			if rec.OldEnd.Before(ar.Start) {
				return fmt.Errorf(
					"retention: 档案 %s 的修订 %s 的原截止日 %s 早于起算日 %s，保存的期限历史不可能由正常办理产生，记录已损坏: %w",
					id, rec.ID, rec.OldEnd, ar.Start, ErrCorruptState)
			}
			if rec.NewEnd.Before(ar.Start) {
				return fmt.Errorf(
					"retention: 档案 %s 的修订 %s 的新截止日 %s 早于起算日 %s，保存的期限历史不可能由正常办理产生，记录已损坏: %w",
					id, rec.ID, rec.NewEnd, ar.Start, ErrCorruptState)
			}
		}
	}
	return nil
}

// validateRevisionIDs 检查成功修订编号在整个保管库内的唯一性，以及与已关闭
// 清册申请编号的互不占用。
//
// 正常办理保存的记录必然满足：每个修订编号在全部档案的修订历史中只出现
// 一次，且没有任何编号同时被用作成功销毁申请的编号。同一档案历史中重复
// 使用同一编号、不同档案各自保存同号修订（即使两条记录的日期、原因等
// 内容完全相同），或修订编号与一份已关闭清册的申请编号相同，都会使
// “按编号重试取回唯一一条修订”无法成立，返回可由 ErrCorruptState 识别
// 的错误：绝不合并重复记录，也不挑选其中一条继续使用。
//
// 错误信息给出冲突的修订编号与涉及的档案编号；跨档案重复时同时给出两份
// 档案，与清册冲突时给出清册的申请编号。校验覆盖整个保管库的全部档案
// （含已销毁的），与本次办理名单或查询目标无关。修订编号为空白同样不能
// 作为成功记录存在，一并按损坏处理。
func validateRevisionIDs(data *storeData) error {
	owners := make(map[string]string)
	// map 遍历顺序不稳定，按档案编号排序后再检查，保证错误信息稳定。
	archiveIDs := make([]string, 0, len(data.Archives))
	for id := range data.Archives {
		archiveIDs = append(archiveIDs, id)
	}
	sort.Strings(archiveIDs)
	for _, id := range archiveIDs {
		ar := data.Archives[id]
		if ar == nil {
			// 缺失的登记记录已由 validateFreezeReleaseRecords 报告。
			continue
		}
		for _, rec := range ar.Revisions {
			if rec == nil {
				// 空记录已由 validateRevisionContinuity 报告。
				continue
			}
			if strings.TrimSpace(rec.ID) == "" {
				return fmt.Errorf(
					"retention: 档案 %s 的修订历史中存在没有编号的修订记录，记录已损坏: %w",
					id, ErrCorruptState)
			}
			if owner, ok := owners[rec.ID]; ok {
				if owner == id {
					return fmt.Errorf(
						"retention: 修订编号 %s 在档案 %s 的历史中出现多条记录，保存记录已损坏: %w",
						rec.ID, id, ErrCorruptState)
				}
				return fmt.Errorf(
					"retention: 修订编号 %s 同时保存在档案 %s 与档案 %s 的修订历史中，保存记录已损坏: %w",
					rec.ID, owner, id, ErrCorruptState)
			}
			owners[rec.ID] = id
		}
	}
	// 修订编号与销毁申请编号互不占用：任何已关闭清册的申请编号都不能
	// 同时是一条已保存修订的编号。申请编号按排序遍历，错误信息稳定。
	appIDs := make([]string, 0, len(data.Manifests))
	for appID := range data.Manifests {
		appIDs = append(appIDs, appID)
	}
	sort.Strings(appIDs)
	for _, appID := range appIDs {
		if owner, ok := owners[appID]; ok {
			return fmt.Errorf(
				"retention: 档案 %s 的修订编号 %s 与已关闭清册的申请编号 %s 相同，保存记录已损坏: %w",
				owner, appID, appID, ErrCorruptState)
		}
	}
	return nil
}

// validateRevisionOriginRecords 检查库内每条已保存修订保留的办理信息是否完整：
// 非空白的修订原因与有效的修订日期必须齐备。
//
// 提交修订时，修订原因（去除首尾空白后不得为空白）与修订日期（真实的
// YYYY-MM-DD 日期）都是必填项，保存下来的每条修订也必须满足同一要求：
// 修订历史必须能说明这次调整是在何时、因何办理的。保存记录中的修订原因
// 缺失、为 null、为空串或仅含空白，都算缺少修订原因；修订日期字段缺失或
// 为 null，都算缺少修订日期。已填写的修订日期仍由 Date 的解析校验守住
// 真实日期要求——不是合法日历日期的值在 JSON 解码阶段就使整库判损坏。
// 缺少原因或日期任一项的修订已无法说明自己因何、于何时办理，不能当作
// 正常记录交给历史查询、销毁前核对与各项办理：返回可由 ErrCorruptState
// 识别的错误，错误信息给出档案编号、修订编号，并说明缺少的是修订原因
// 还是修订日期；同一条修订两项同时缺少时两项都说明。
//
// 这项要求覆盖每一条成功修订，而不只是最后一条：一份档案先延长、随后又
// 缩短时，中间那次修订缺少原因，不能因为最终截止日合法、全部期限前后
// 衔接就接受它。校验覆盖整个保管库全部档案的全部修订历史——仍被冻结或
// 已经销毁档案的修订同样检查：清册的归属、条目与处理日期再正确，也不能
// 掩盖修订办理信息缺项；不能补写原因、借用冻结或销毁日期，也不能删除
// 那次修订来继续使用。绝不自动补写日期或原因或删除修订，原保存内容保持
// 原样。没有修订记录的旧档案是合法记录，不在此报错，仍沿用既有兼容行为。
//
// load 在每次查询、核对与办理前都会重新执行校验，因此保管库打开后保存
// 内容才出现这种缺项时，下一次使用有效输入（即使本次查询、核对或办理的
// 是另一份正常档案）也按整库记录损坏失败：不返回正常历史或部分核对结果，
// 不产生业务变更，原保存内容保持原样。
func validateRevisionOriginRecords(data *storeData) error {
	// map 遍历顺序不稳定，按档案编号排序、修订按成功办理顺序检查，
	// 保证错误信息稳定。
	archiveIDs := make([]string, 0, len(data.Archives))
	for id := range data.Archives {
		archiveIDs = append(archiveIDs, id)
	}
	sort.Strings(archiveIDs)
	for _, id := range archiveIDs {
		ar := data.Archives[id]
		if ar == nil {
			// 缺失的登记记录已由前面的校验报告。
			continue
		}
		for _, rec := range ar.Revisions {
			if rec == nil {
				// 空修订记录已由 validateRevisionContinuity 报告。
				continue
			}
			reasonMissing := strings.TrimSpace(rec.Reason) == ""
			dateMissing := rec.RevisedOn == nil || rec.RevisedOn.IsZero()
			switch {
			case reasonMissing && dateMissing:
				return fmt.Errorf(
					"retention: 档案 %s 的修订 %s 缺少修订原因与修订日期，记录已损坏: %w",
					id, rec.ID, ErrCorruptState)
			case reasonMissing:
				return fmt.Errorf(
					"retention: 档案 %s 的修订 %s 缺少修订原因，记录已损坏: %w",
					id, rec.ID, ErrCorruptState)
			case dateMissing:
				return fmt.Errorf(
					"retention: 档案 %s 的修订 %s 缺少修订日期，记录已损坏: %w",
					id, rec.ID, ErrCorruptState)
			}
		}
	}
	return nil
}

// save 原子地写入状态：先写同目录临时文件并刷盘，再 rename 替换，最后刷目录。
// 写入过程中崩溃不会留下半截状态，旧文件保持完好。
func (s *Store) save(data *storeData) error {
	buf, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("retention: 状态编码失败: %w", err)
	}
	tmp, err := os.CreateTemp(s.dir, ".retention-state-*.tmp")
	if err != nil {
		return fmt.Errorf("retention: 无法创建临时状态文件: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(buf); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("retention: 写入状态失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("retention: 刷盘状态失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("retention: 关闭状态文件失败: %w", err)
	}
	if err := os.Rename(tmpName, s.statePath()); err != nil {
		cleanup()
		return fmt.Errorf("retention: 替换状态文件失败: %w", err)
	}
	// 刷目录，确保 rename 在崩溃后仍然落盘。
	if dir, err := os.Open(s.dir); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

// mutate 在跨进程排他锁内重读状态、办理变更，仅在全部校验通过后写回。
// fn 返回错误时绝不写盘，磁盘上的已有记录不受影响。
func (s *Store) mutate(fn func(*storeData) error) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	data, err := s.load()
	if err != nil {
		return err
	}
	if err := fn(data); err != nil {
		return err
	}
	return s.save(data)
}

// view 在共享锁内重读状态并执行只读查询。
func (s *Store) view(fn func(*storeData) error) error {
	s.opMu.RLock()
	defer s.opMu.RUnlock()
	unlock, err := s.rlock()
	if err != nil {
		return err
	}
	defer unlock()
	data, err := s.load()
	if err != nil {
		return err
	}
	return fn(data)
}
