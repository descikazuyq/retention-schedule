package retention

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// storeData 是落盘的全部数据。
type storeData struct {
	Archives    map[string]*archiveData   `json:"archives"`
	Inventories map[string]*inventoryData `json:"inventories"`
}

// archiveData 是档案的内部持久化结构。
type archiveData struct {
	Number    string        `json:"number"`
	Category  string        `json:"category"`
	StartDate Date          `json:"start_date"`
	Deadline  Date          `json:"deadline"`
	Destroyed bool          `json:"destroyed"`
	Inventory string        `json:"inventory,omitempty"`
	Freezes   []*freezeData `json:"freezes"`
}

// freezeData 是冻结记录的内部持久化结构。
type freezeData struct {
	Number   string        `json:"number"`
	Reason   string        `json:"reason"`
	Date     Date          `json:"date"`
	Unfreeze *unfreezeData `json:"unfreeze,omitempty"`
}

// unfreezeData 是解除记录的内部持久化结构。
type unfreezeData struct {
	Reason string `json:"reason"`
	Date   Date   `json:"date"`
}

// inventoryData 是清册的内部持久化结构。
type inventoryData struct {
	Application string               `json:"application_number"`
	ProcessDate Date                 `json:"process_date"`
	Items       []*inventoryItemData `json:"items"`
}

// inventoryItemData 是清册快照的内部持久化结构。
type inventoryItemData struct {
	Number    string `json:"number"`
	Category  string `json:"category"`
	StartDate Date   `json:"start_date"`
	Deadline  Date   `json:"deadline"`
}

// readData 从保存位置读取全部数据；文件不存在时返回空数据集。
func readData(path string) (*storeData, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &storeData{
			Archives:    make(map[string]*archiveData),
			Inventories: make(map[string]*inventoryData),
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("retention: 读取保管数据失败: %w", err)
	}
	var data storeData
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, fmt.Errorf("retention: 保管数据损坏: %w", err)
	}
	if data.Archives == nil {
		data.Archives = make(map[string]*archiveData)
	}
	if data.Inventories == nil {
		data.Inventories = make(map[string]*inventoryData)
	}
	return &data, nil
}

// writeData 原子写入：先写临时文件、落盘，再重命名，
// 避免中途崩溃留下半份数据。
func writeData(path string, data *storeData) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("retention: 写入保管数据失败: %w", err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(data); err != nil {
		f.Close()
		return fmt.Errorf("retention: 编码保管数据失败: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("retention: 同步保管数据失败: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("retention: 替换保管数据失败: %w", err)
	}
	// 同步目录项，保证重命名本身落盘。
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
