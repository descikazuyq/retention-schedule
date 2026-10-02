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
	RetentionEnd          Date
	Destroyed             bool
	ManifestApplicationID string
	// Freezes 包含全部冻结及解除历史，按登记顺序排列。
	Freezes []FreezeRecord
	// ActiveFreezes 仅包含尚未解除的冻结。
	ActiveFreezes []FreezeRecord
	// Manifest 是该档案销毁时生成的已关闭清册；未销毁时为 nil。
	Manifest *Manifest
}

// 以下类型是持久化到磁盘的数据结构，仅在包内使用。

type archiveRecord struct {
	ID         string          `json:"id"`
	Category   string          `json:"category"`
	Start      Date            `json:"start"`
	End        Date            `json:"end"`
	Destroyed  bool            `json:"destroyed"`
	ManifestID string          `json:"manifest_id,omitempty"`
	Freezes    []*freezeRecord `json:"freezes"`
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
}

func newStoreData() *storeData {
	return &storeData{
		Version:   1,
		Archives:  map[string]*archiveRecord{},
		Manifests: map[string]*manifestRecord{},
	}
}
