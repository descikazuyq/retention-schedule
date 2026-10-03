package retention

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
// 还拼接了其他内容，或已解除冻结的解除原因、解除日期缺失或解除日期早于
// 冻结日期等）时返回 ErrCorruptState，不会返回可继续办理的保管库，
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
// 内容能解析时还要核对全部已解除冻结的解除信息：任一冻结标为已解除
// 但解除原因为空白、解除日期缺失或早于冻结日期，整份保管库同样判为
// 记录损坏（见 validateReleasedFreezes）。
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
	// 兼容引入修订功能之前保存的保管库：没有记录最初截止日时，
	// 登记截止日就是最初截止日。
	for _, ar := range data.Archives {
		if ar.InitialEnd.IsZero() {
			ar.InitialEnd = ar.End
		}
	}
	if err := validateReleasedFreezes(data); err != nil {
		return nil, err
	}
	return data, nil
}

// validateReleasedFreezes 核对库内全部已解除冻结的解除信息是否完整有效。
//
// 解除功能在写入时要求填写解除日期与原因，且解除日期不得早于冻结日期；
// 读取已保存记录时同样遵守这些要求：只要任一档案（包括已销毁档案）存在
// 标为已解除但解除原因为空白、解除日期缺失或早于冻结日期的冻结，整份
// 保管库即判为记录损坏，返回 ErrCorruptState。未解除的冻结没有解除日期
// 和原因是合法状态，不在此列。
func validateReleasedFreezes(data *storeData) error {
	// 按档案编号排序遍历，保证同一份损坏内容报告的定位信息稳定。
	ids := make([]string, 0, len(data.Archives))
	for id := range data.Archives {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		ar := data.Archives[id]
		for _, fr := range ar.Freezes {
			if !fr.Released {
				continue
			}
			if normalizeText(fr.ReleaseReason) == "" {
				return fmt.Errorf("retention: 档案 %s 的冻结 %s 已解除但解除原因缺失: %w",
					ar.ID, fr.ID, ErrCorruptState)
			}
			if fr.ReleasedOn == nil || fr.ReleasedOn.IsZero() {
				return fmt.Errorf("retention: 档案 %s 的冻结 %s 已解除但解除日期缺失: %w",
					ar.ID, fr.ID, ErrCorruptState)
			}
			if fr.ReleasedOn.Before(fr.FrozenOn) {
				return fmt.Errorf("retention: 档案 %s 的冻结 %s 解除日期 %s 早于冻结日期 %s: %w",
					ar.ID, fr.ID, fr.ReleasedOn, fr.FrozenOn, ErrCorruptState)
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
