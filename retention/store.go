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
// 缺少解除原因或解除日期、解除日期早于冻结日期，或已保存的销毁状态与关闭
// 清册不能相互对应：已销毁档案找不到唯一收录它的所属清册、同一档案出现在
// 多份清册中、未销毁档案仍挂有清册申请编号、清册收录了不存在或未销毁的
// 档案等）时返回 ErrCorruptState，不会返回可继续办理的保管库，已有记录
// 保持原样，不会被清空、修补或覆盖。
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
// JSON 能解析不代表记录合法。load 还会检查两类业务不变量：
//
// 其一，任何已解除冻结都必须同时带有非空白的解除原因和有效的解除日期，
// 且解除日期不早于冻结日期——与解除功能办理时的要求一致。
//
// 其二，已保存的销毁状态必须与关闭清册严格相互对应：
//   - 每份已销毁档案必须记下唯一一份所属清册的申请编号，该清册存在，
//     且清册恰好收录该档案一次；档案记下的编号找不到清册、清册没有
//     收录它、或同一档案在同一份清册中出现多次，都属于关系损坏；
//   - 每份未销毁档案不得挂有任何清册申请编号；
//   - 每份清册中的每份档案都必须存在、标记为已销毁，并且其记下的
//     所属申请编号恰好指向这份清册（同一档案出现在不同清册中即损坏）。
//
// 任一不变量不成立时，整份保管库判为损坏并返回 ErrCorruptState，
// 错误信息指明涉及的档案编号与相关申请编号。
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
	// 已保存的销毁状态与关闭清册必须严格相互对应：缺少任一侧对应关系，
	// 已销毁记录就无法说明档案究竟由哪次申请销毁，绝不能任选一份记录
	// 继续办理（例如把清册仍在却被标成未销毁的档案再次销毁）。
	if err := validateManifestConsistency(data); err != nil {
		return nil, err
	}
	// 兼容引入修订功能之前保存的保管库：没有记录最初截止日时，
	// 登记截止日就是最初截止日。
	for _, ar := range data.Archives {
		if ar.InitialEnd.IsZero() {
			ar.InitialEnd = ar.End
		}
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

// validateManifestConsistency 检查库内销毁状态与关闭清册是否严格相互对应。
//
// 正常销毁保存的记录必然满足：每份已销毁档案记下的申请编号能找到一份
// 清册，该清册存在且恰好收录它一次；未销毁档案不挂任何清册申请编号；
// 每份清册收录的每份档案都存在、标记为已销毁，并且其记下的所属申请
// 编号恰好指向这份清册。
//
// 任一对应关系缺失或冲突——已销毁档案没有申请编号或找不到所属清册、
// 清册未收录它或重复收录、未销毁档案仍挂有清册编号、清册收录了不存在
// 或未销毁的档案、同一档案出现在不同清册中或归属指向别的申请——都说明
// 保存内容已损坏：这些记录已无法说明档案究竟由哪次申请销毁，不能任选
// 其中一份继续办理。此时返回可由 ErrCorruptState 识别的错误，信息中
// 给出涉及的档案编号与相关申请编号。校验覆盖整个保管库，与本次查询或
// 办理的档案无关。
func validateManifestConsistency(data *storeData) error {
	// 第一遍以档案保存的销毁状态为准，检查它与所属清册的对应。
	// map 遍历顺序不稳定，按编号排序后再检查，保证错误信息稳定。
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
		manifestID := strings.TrimSpace(ar.ManifestID)
		switch {
		case ar.Destroyed && manifestID == "":
			return fmt.Errorf(
				"retention: 档案 %s 标记为已销毁但没有所属清册申请编号，记录已损坏: %w",
				id, ErrCorruptState)
		case !ar.Destroyed && manifestID != "":
			return fmt.Errorf(
				"retention: 档案 %s 未销毁但仍挂有清册申请编号 %s，记录已损坏: %w",
				id, ar.ManifestID, ErrCorruptState)
		}
		if !ar.Destroyed {
			continue
		}
		rec, ok := data.Manifests[ar.ManifestID]
		if !ok {
			return fmt.Errorf(
				"retention: 档案 %s 标记为已销毁，但其所属申请 %s 的清册已缺失，记录已损坏: %w",
				id, ar.ManifestID, ErrCorruptState)
		}
		count := 0
		for _, e := range rec.Entries {
			if e.ID == id {
				count++
			}
		}
		switch {
		case count == 0:
			return fmt.Errorf(
				"retention: 档案 %s 标记为已销毁并指向申请 %s，但该清册未收录此档案，记录已损坏: %w",
				id, ar.ManifestID, ErrCorruptState)
		case count > 1:
			return fmt.Errorf(
				"retention: 档案 %s 在申请 %s 的清册中被收录 %d 次，每份档案只能归属一份清册一次，记录已损坏: %w",
				id, ar.ManifestID, count, ErrCorruptState)
		}
	}

	// 第二遍以清册为准，检查每份收录档案都存在、已销毁且反向指向本清册。
	// 同一档案出现在另一份清册中时，其反向归属必然与本申请编号冲突。
	appIDs := make([]string, 0, len(data.Manifests))
	for appID := range data.Manifests {
		appIDs = append(appIDs, appID)
	}
	sort.Strings(appIDs)
	for _, appID := range appIDs {
		rec := data.Manifests[appID]
		if rec == nil {
			return fmt.Errorf("retention: 申请 %s 的清册记录缺失，状态文件已损坏: %w",
				appID, ErrCorruptState)
		}
		if strings.TrimSpace(rec.ApplicationID) != appID {
			return fmt.Errorf(
				"retention: 申请 %s 的清册记录编号为 %q，清册归属已损坏: %w",
				appID, rec.ApplicationID, ErrCorruptState)
		}
		for _, e := range rec.Entries {
			ar, ok := data.Archives[e.ID]
			if !ok {
				return fmt.Errorf(
					"retention: 申请 %s 的清册收录了不存在的档案 %s，记录已损坏: %w",
					appID, e.ID, ErrCorruptState)
			}
			if !ar.Destroyed || strings.TrimSpace(ar.ManifestID) == "" {
				return fmt.Errorf(
					"retention: 申请 %s 的清册收录了未销毁的档案 %s，记录已损坏: %w",
					appID, e.ID, ErrCorruptState)
			}
			if ar.ManifestID != appID {
				return fmt.Errorf(
					"retention: 档案 %s 同时涉及申请 %s 与 %s，销毁归属相互冲突，记录已损坏: %w",
					e.ID, appID, ar.ManifestID, ErrCorruptState)
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
