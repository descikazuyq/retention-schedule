package retention

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Store 是一个本地保管位置。
//
// 同一时刻只允许一个进程内的一个 goroutine 执行写操作；
// 跨进程则通过保存位置中的锁文件串行。
// 操作在锁内读取最新数据、校验、落盘，因此两个本机程序
// 同时办理同一档案的冻结或销毁时，结果必有先后。
type Store struct {
	dir      string
	dataPath string
	lockPath string

	mu       sync.Mutex
	lockFile *os.File
	closed   bool
}

// Open 打开（或创建）一个本地保管位置。
// dir 是调用者选择的本地保存位置；不存在时会创建。
func Open(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("%w: 保存位置不能为空", ErrBlankField)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("retention: 创建保存位置失败: %w", err)
	}
	lockPath := filepath.Join(dir, "store.lock")
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("retention: 打开锁文件失败: %w", err)
	}
	s := &Store{
		dir:      dir,
		dataPath: filepath.Join(dir, "store.json"),
		lockPath: lockPath,
		lockFile: lf,
	}
	// 打开时先读一次，尽早暴露损坏的数据文件。
	if _, err := readData(s.dataPath); err != nil {
		lf.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭保管位置，释放锁文件。
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var err error
	if s.lockFile != nil {
		err = s.lockFile.Close()
		s.lockFile = nil
	}
	return err
}

// view 在锁内读取数据并执行 fn，不落盘。
func (s *Store) view(fn func(*storeData) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrStoreClosed
	}
	if err := lockFile(s.lockFile.Fd()); err != nil {
		return fmt.Errorf("retention: 获取文件锁失败: %w", err)
	}
	defer func() { _ = unlockFile(s.lockFile.Fd()) }()

	data, err := readData(s.dataPath)
	if err != nil {
		return err
	}
	return fn(data)
}

// withLock 在锁内读取数据、执行 fn，成功后原子落盘。
// fn 返回错误时不落盘，调用方观察到的就是操作前的完整状态。
func (s *Store) withLock(fn func(*storeData) error) error {
	return s.view(func(data *storeData) error {
		if err := fn(data); err != nil {
			return err
		}
		return writeData(s.dataPath, data)
	})
}

// Register 登记一条档案。
//
// number 是唯一档案编号，category 是类别，startDate 是起算日，
// deadline 是保管截止日；日期均为 YYYY-MM-DD。
// 编号或类别空白、日期无效、截止日早于起算日、编号重复都会明确失败，
// 已有记录不受影响。
func (s *Store) Register(number, category, startDate, deadline string) error {
	if strings.TrimSpace(number) == "" {
		return fmt.Errorf("%w: 档案编号不能为空白", ErrBlankField)
	}
	if strings.TrimSpace(category) == "" {
		return fmt.Errorf("%w: 类别不能为空白", ErrBlankField)
	}
	sd, err := ParseDate(startDate)
	if err != nil {
		return err
	}
	dd, err := ParseDate(deadline)
	if err != nil {
		return err
	}
	if dd.Time.Before(sd.Time) {
		return fmt.Errorf("%w: 保管截止日 %s 早于起算日 %s", ErrDeadlineBeforeStart, dd, sd)
	}

	return s.withLock(func(data *storeData) error {
		if _, ok := data.Archives[number]; ok {
			return fmt.Errorf("%w: %q", ErrDuplicateNumber, number)
		}
		data.Archives[number] = &archiveData{
			Number:    number,
			Category:  category,
			StartDate: sd,
			Deadline:  dd,
		}
		return nil
	})
}

// Freeze 为档案记录一条冻结。
//
// freezeNumber 是冻结编号，在同一档案内唯一；reason 是冻结原因，
// freezeDate 是冻结日期。档案不存在、已销毁、冻结编号重复、
// 原因空白或日期无效都会明确失败。
func (s *Store) Freeze(archiveNumber, freezeNumber, reason, freezeDate string) error {
	if strings.TrimSpace(freezeNumber) == "" {
		return fmt.Errorf("%w: 冻结编号不能为空白", ErrBlankField)
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: 冻结原因不能为空白", ErrBlankField)
	}
	fd, err := ParseDate(freezeDate)
	if err != nil {
		return err
	}

	return s.withLock(func(data *storeData) error {
		a, ok := data.Archives[archiveNumber]
		if !ok {
			return fmt.Errorf("%w: %q", ErrArchiveNotFound, archiveNumber)
		}
		if a.Destroyed {
			return fmt.Errorf("%w: %q", ErrArchiveDestroyed, archiveNumber)
		}
		for _, f := range a.Freezes {
			if f.Number == freezeNumber {
				return fmt.Errorf("%w: 档案 %q 下冻结编号 %q 已存在", ErrDuplicateFreezeNumber, archiveNumber, freezeNumber)
			}
		}
		a.Freezes = append(a.Freezes, &freezeData{
			Number: freezeNumber,
			Reason: reason,
			Date:   fd,
		})
		return nil
	})
}

// Unfreeze 解除一条冻结。
//
// reason 是解除原因，unfreezeDate 是解除日期；解除日期不能早于冻结日期。
// 冻结不存在、已解除、原因空白或日期无效都会明确失败。
// 解除只追加解除记录，不删除冻结历史；一条冻结的解除不影响其他冻结。
func (s *Store) Unfreeze(archiveNumber, freezeNumber, reason, unfreezeDate string) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: 解除原因不能为空白", ErrBlankField)
	}
	ud, err := ParseDate(unfreezeDate)
	if err != nil {
		return err
	}

	return s.withLock(func(data *storeData) error {
		a, ok := data.Archives[archiveNumber]
		if !ok {
			return fmt.Errorf("%w: %q", ErrArchiveNotFound, archiveNumber)
		}
		var target *freezeData
		for _, f := range a.Freezes {
			if f.Number == freezeNumber {
				target = f
				break
			}
		}
		if target == nil {
			return fmt.Errorf("%w: 档案 %q 下不存在冻结 %q", ErrFreezeNotFound, archiveNumber, freezeNumber)
		}
		if target.Unfreeze != nil {
			return fmt.Errorf("%w: 档案 %q 的冻结 %q 已解除", ErrAlreadyUnfrozen, archiveNumber, freezeNumber)
		}
		if ud.Time.Before(target.Date.Time) {
			return fmt.Errorf("%w: 解除日期 %s 早于冻结日期 %s", ErrUnfreezeBeforeFreeze, ud, target.Date)
		}
		target.Unfreeze = &unfreezeData{Reason: reason, Date: ud}
		return nil
	})
}

// Destroy 一次销毁多个档案，并生成一份已关闭清册。
//
// applicationNumber 是唯一申请编号，processDate 是本次处理日期，
// archiveNumbers 是本次销毁的档案编号名单。
// 只有名单非空、编号不重复，且所选档案全部存在、到期（截止日当天算到期）、
// 未销毁、没有未解除的冻结，才会同时完成销毁并生成清册；
// 任一条件不满足都整次失败，不留下部分销毁或清册。
//
// 已成功的申请再次提交时，若申请编号、处理日期和档案集合都相同
// （名单顺序不影响判断），返回原清册；沿用编号但改变日期或档案集合
// 则明确失败。失败的申请在条件改变后可用同一编号再次提交。
func (s *Store) Destroy(applicationNumber, processDate string, archiveNumbers ...string) (*Inventory, error) {
	if strings.TrimSpace(applicationNumber) == "" {
		return nil, fmt.Errorf("%w: 申请编号不能为空白", ErrBlankField)
	}
	pd, err := ParseDate(processDate)
	if err != nil {
		return nil, err
	}
	if len(archiveNumbers) == 0 {
		return nil, ErrEmptyDestroyList
	}
	seen := make(map[string]bool, len(archiveNumbers))
	for _, n := range archiveNumbers {
		if strings.TrimSpace(n) == "" {
			return nil, fmt.Errorf("%w: 档案编号不能为空白", ErrBlankField)
		}
		if seen[n] {
			return nil, fmt.Errorf("%w: 名单中档案编号 %q 重复", ErrDuplicateInList, n)
		}
		seen[n] = true
	}

	var result *Inventory
	err = s.withLock(func(data *storeData) error {
		// 幂等：申请编号已存在时，只接受完全相同的申请。
		if existing, ok := data.Inventories[applicationNumber]; ok {
			if !existing.ProcessDate.Time.Equal(pd.Time) || !sameItemSet(existing.Items, archiveNumbers) {
				return fmt.Errorf("%w: 申请 %q 已存在，但处理日期或档案集合与原清册不一致", ErrApplicationConflict, applicationNumber)
			}
			result = inventoryToRecord(existing)
			return nil
		}

		items := make([]*inventoryItemData, 0, len(archiveNumbers))
		archives := make([]*archiveData, 0, len(archiveNumbers))
		for _, n := range archiveNumbers {
			a, ok := data.Archives[n]
			if !ok {
				return fmt.Errorf("%w: %q", ErrArchiveNotFound, n)
			}
			if a.Destroyed {
				return fmt.Errorf("%w: 档案 %q 已在清册 %q 中销毁", ErrArchiveDestroyed, n, a.Inventory)
			}
			// 截止日当天即视为到期：处理日早于截止日才是未到期。
			if pd.Time.Before(a.Deadline.Time) {
				return fmt.Errorf("%w: 档案 %q 在处理日 %s 尚未到期（保管截止日 %s）", ErrNotExpired, n, pd, a.Deadline)
			}
			for _, f := range a.Freezes {
				if f.Unfreeze == nil {
					return fmt.Errorf("%w: 档案 %q 存在未解除冻结 %q", ErrFrozen, n, f.Number)
				}
			}
			items = append(items, &inventoryItemData{
				Number:    a.Number,
				Category:  a.Category,
				StartDate: a.StartDate,
				Deadline:  a.Deadline,
			})
			archives = append(archives, a)
		}

		inv := &inventoryData{
			Application: applicationNumber,
			ProcessDate: pd,
			Items:       items,
		}
		data.Inventories[applicationNumber] = inv
		for _, a := range archives {
			a.Destroyed = true
			a.Inventory = applicationNumber
		}
		result = inventoryToRecord(inv)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Archive 按档案编号核对：返回登记内容、全部冻结及解除历史、
// 当前冻结和销毁状态，以及对应的清册。未登记的编号返回 ErrArchiveNotFound。
func (s *Store) Archive(archiveNumber string) (*ArchiveRecord, error) {
	var result *ArchiveRecord
	err := s.view(func(data *storeData) error {
		a, ok := data.Archives[archiveNumber]
		if !ok {
			return fmt.Errorf("%w: %q", ErrArchiveNotFound, archiveNumber)
		}
		result = archiveToRecord(a)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Archives 返回全部档案的核对视图，按档案编号排序。
func (s *Store) Archives() ([]*ArchiveRecord, error) {
	var result []*ArchiveRecord
	err := s.view(func(data *storeData) error {
		result = make([]*ArchiveRecord, 0, len(data.Archives))
		for _, a := range data.Archives {
			result = append(result, archiveToRecord(a))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Number < result[j].Number })
	return result, nil
}

// Inventory 按申请编号返回已关闭清册。清册不存在时返回 ErrInventoryNotFound。
func (s *Store) Inventory(applicationNumber string) (*Inventory, error) {
	var result *Inventory
	err := s.view(func(data *storeData) error {
		inv, ok := data.Inventories[applicationNumber]
		if !ok {
			return fmt.Errorf("%w: %q", ErrInventoryNotFound, applicationNumber)
		}
		result = inventoryToRecord(inv)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Inventories 返回全部已关闭清册，按申请编号排序。
func (s *Store) Inventories() ([]*Inventory, error) {
	var result []*Inventory
	err := s.view(func(data *storeData) error {
		result = make([]*Inventory, 0, len(data.Inventories))
		for _, inv := range data.Inventories {
			result = append(result, inventoryToRecord(inv))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ApplicationNumber < result[j].ApplicationNumber })
	return result, nil
}

// sameItemSet 判断清册内的档案编号集合与名单集合是否相同，忽略顺序。
func sameItemSet(items []*inventoryItemData, numbers []string) bool {
	if len(items) != len(numbers) {
		return false
	}
	inItems := make(map[string]bool, len(items))
	for _, it := range items {
		inItems[it.Number] = true
	}
	for _, n := range numbers {
		if !inItems[n] {
			return false
		}
	}
	return true
}

func archiveToRecord(a *archiveData) *ArchiveRecord {
	rec := &ArchiveRecord{
		Number:    a.Number,
		Category:  a.Category,
		StartDate: a.StartDate,
		Deadline:  a.Deadline,
		Destroyed: a.Destroyed,
		Inventory: a.Inventory,
	}
	for _, f := range a.Freezes {
		fr := FreezeRecord{Number: f.Number, Reason: f.Reason, Date: f.Date}
		if f.Unfreeze != nil {
			fr.Unfreeze = &UnfreezeRecord{Reason: f.Unfreeze.Reason, Date: f.Unfreeze.Date}
		}
		rec.Freezes = append(rec.Freezes, fr)
	}
	return rec
}

func inventoryToRecord(inv *inventoryData) *Inventory {
	out := &Inventory{
		ApplicationNumber: inv.Application,
		ProcessDate:       inv.ProcessDate,
	}
	for _, it := range inv.Items {
		out.Items = append(out.Items, InventoryItem{
			Number:    it.Number,
			Category:  it.Category,
			StartDate: it.StartDate,
			Deadline:  it.Deadline,
		})
	}
	return out
}
