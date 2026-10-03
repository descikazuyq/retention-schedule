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
// 还拼接了其他内容等），或其中保存的记录不满足业务不变量（例如已解除冻结
// 缺少解除原因或解除日期、解除日期早于冻结日期、已销毁档案与已关闭清册
// 对应不上、修订编号在全库不唯一或与已关闭清册的申请编号相同）时返回
// ErrCorruptState，
// 不会返回可继续办理的保管库，已有记录保持原样，不会被清空、修补或覆盖。
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
// JSON 能解析不代表记录合法：任何已解除冻结都必须同时带有非空白的
// 解除原因和有效的解除日期，且解除日期不早于冻结日期——与解除功能
// 办理时的要求一致。缺少任一信息或日期顺序不成立时，整份保管库同样
// 判为损坏并返回 ErrCorruptState，错误信息指明涉及的档案与冻结编号。
//
// 已销毁档案与已关闭清册也必须相互对应：每份已销毁档案记下的申请编号
// 必须能找到一份清册，且该清册恰好收录该档案一次；清册收录的每份档案
// 也必须存在、标成已销毁并指回这份清册。已销毁档案找不到清册、同一档案
// 出现在多份清册、归属指向别的申请，或未销毁档案仍挂有清册申请编号，
// 都说明记录已无法说明档案由哪次申请销毁，整份保管库判为损坏并返回
// ErrCorruptState，错误信息指明涉及的档案编号与相关申请编号。
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
// 每个成功的修订编号在全库也只能对应一条已保存的修订记录，且不得与
// 已关闭清册的申请编号相同：同一档案的历史中重复出现同一编号、不同档案
// 各自保存同号修订（无论内容是否相同），或修订编号等于某份清册的申请
// 编号，按编号都无法唯一确定应取回哪条记录，整份保管库判为损坏并返回
// ErrCorruptState，错误信息指明冲突编号与涉及的档案编号（跨档案重复时
// 两份档案都给出，涉及清册时给出申请编号）。绝不合并重复记录或挑选
// 其中一条继续使用。
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
		return nil, fmt.Errorf("retention: 状态文件内容无法解析: %v: %w", err, ErrCorruptState)
	}
	if data.Archives == nil {
		data.Archives = map[string]*archiveRecord{}
	}
	if data.Manifests == nil {
		data.Manifests = map[string]*manifestRecord{}
	}
	// 语义校验先于任何兼容处理：已解除冻结必须与既有解除功能遵守同一要求——
	// 解除原因与解除日期齐备，且解除日期不早于冻结日期。
	// 只有“已解除”标记而缺少任一信息，或解除日期早于冻结日期的记录
	// 一律判为损坏（即使所属档案已销毁）；绝不据此继续办理或修补记录。
	if err := validateFreezeReleaseRecords(data); err != nil {
		return nil, err
	}
	// 已销毁档案与已关闭清册必须相互对应，否则无法说明档案由哪次申请销毁。
	// 关系损坏时绝不挑选其中一份记录继续使用，也不补清册或改销毁标记。
	if err := validateManifestConsistency(data); err != nil {
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
	// 最初截止日、修订记录与当前截止日必须连续衔接，否则当前期限与
	// 历史期限相互矛盾，任何一个日期都不能当作核对依据。
	if err := validateRevisionContinuity(data); err != nil {
		return nil, err
	}
	// 每个成功的修订编号在全库只能对应一条已保存的修订记录，且不得
	// 与已关闭清册的申请编号相同，否则按编号无法唯一确定应取回哪条记录。
	if err := validateRevisionIDUniqueness(data); err != nil {
		return nil, err
	}
	return data, nil
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
			case fr.ReleasedOn.Before(fr.FrozenOn):
				return fmt.Errorf(
					"retention: 档案 %s 的冻结 %s 的解除日期 %s 早于冻结日期 %s，记录已损坏: %w",
					id, fr.ID, fr.ReleasedOn, fr.FrozenOn, ErrCorruptState)
			}
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

// validateRevisionIDUniqueness 检查每个成功的修订编号在整个保管库内
// 只对应一条已保存的修订记录，且不与任何已关闭清册的申请编号相同。
//
// 正常办理保存的记录必然满足：修订编号全库唯一，与销毁申请编号互不占用。
// 同一档案的历史中重复出现同一编号、不同档案各自保存同号修订（无论两条
// 记录内容是否相同），或修订编号与一份已关闭清册的申请编号相同，都说明
// 按编号已无法唯一确定应取回哪条记录，保存内容已损坏，返回可由
// ErrCorruptState 识别的错误。错误信息给出冲突编号与涉及的档案编号；
// 跨档案重复时同时给出两份档案，涉及清册时给出申请编号。绝不合并重复
// 记录或挑选其中一条继续使用。校验覆盖整个保管库的全部档案（含已销毁的）
// 与全部清册，与本次办理名单无关。
func validateRevisionIDUniqueness(data *storeData) error {
	// map 遍历顺序不稳定，按档案编号排序后再检查，保证错误信息稳定。
	archiveIDs := make([]string, 0, len(data.Archives))
	for id := range data.Archives {
		archiveIDs = append(archiveIDs, id)
	}
	sort.Strings(archiveIDs)
	// 修订编号 -> 首次保存该编号的档案编号。
	owners := make(map[string]string)
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
			first, dup := owners[rec.ID]
			if !dup {
				owners[rec.ID] = id
				continue
			}
			if first == id {
				return fmt.Errorf(
					"retention: 档案 %s 的修订历史中编号 %s 重复出现，记录已损坏: %w",
					id, rec.ID, ErrCorruptState)
			}
			return fmt.Errorf(
				"retention: 修订编号 %s 同时出现在档案 %s 与档案 %s 的历史中，记录已损坏: %w",
				rec.ID, first, id, ErrCorruptState)
		}
	}

	// 修订编号与已关闭清册的申请编号也必须互不占用。
	applicationIDs := make([]string, 0, len(data.Manifests))
	for id := range data.Manifests {
		applicationIDs = append(applicationIDs, id)
	}
	sort.Strings(applicationIDs)
	for _, appID := range applicationIDs {
		if owner, ok := owners[appID]; ok {
			return fmt.Errorf(
				"retention: 档案 %s 的修订编号 %s 与已关闭清册的申请编号 %s 相同，记录已损坏: %w",
				owner, appID, appID, ErrCorruptState)
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
