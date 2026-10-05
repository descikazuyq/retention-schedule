package retention

import (
	"errors"
	"strings"
	"testing"
)

// 保存的档案集合中，用于查找每份档案的编号（集合键）与该份登记内容里的 id
// 都必须非空白且是同一个编号。id 缺失、为 null、为空串、只有空白，或两处
// 写了不同编号（含仅大小写或首尾空白不同），都属于保存记录损坏：
// Open 必须按 ErrCorruptState 失败，不交付可继续办理的保管库，原文件保持原样。
func TestOpenRejectsArchiveIdentityCorruption(t *testing.T) {
	archive := func(idField string) string {
		if idField == "" {
			return `{"category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","freezes":[]}`
		}
		return `{` + idField + `,"category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","freezes":[]}`
	}
	state := func(key, rec string) string {
		return `{"version":1,"archives":{` + key + `:` + rec + `},"manifests":{}}`
	}

	corrupt := map[string]struct {
		state    string
		wantText []string
	}{
		"查找编号与记录 id 不同": {
			state(`"A-1"`, archive(`"id":"A-2"`)),
			[]string{"不一致", "A-1", "A-2"},
		},
		"id 仅大小写不同": {
			state(`"A-1"`, archive(`"id":"a-1"`)),
			[]string{"不一致", "A-1", "a-1"},
		},
		"id 尾部多空白": {
			state(`"A-1"`, archive(`"id":"A-1 "`)),
			[]string{"不一致", "A-1"},
		},
		"id 首部多空白": {
			state(`"A-1"`, archive(`"id":" A-1"`)),
			[]string{"不一致", "A-1"},
		},
		"id 字段缺失": {
			state(`"A-1"`, archive(``)),
			[]string{"A-1", "缺少有效的 id"},
		},
		"id 为 null": {
			state(`"A-1"`, archive(`"id":null`)),
			[]string{"A-1", "缺少有效的 id"},
		},
		"id 为空串": {
			state(`"A-1"`, archive(`"id":""`)),
			[]string{"A-1", "缺少有效的 id"},
		},
		"id 只有空白": {
			state(`"A-1"`, archive(`"id":"  \t "`)),
			[]string{"A-1", "缺少有效的 id"},
		},
		"查找编号为空串": {
			state(`""`, archive(`"id":""`)),
			[]string{"空白", "查找编号"},
		},
		"查找编号只有空白": {
			state(`"  "`, archive(`"id":"  "`)),
			[]string{"空白", "查找编号"},
		},
	}
	for name, tc := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, tc.state)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("档案身份矛盾时不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			for _, want := range tc.wantText {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("错误信息应包含 %q，得到 %v", want, err)
				}
			}
			if got := readStateFile(t, dir); got != tc.state {
				t.Fatalf("打开失败后原文件被改动: %q -> %q", tc.state, got)
			}
		})
	}
}

// 编号矛盾不能被其他合法信息掩盖：被冻结的档案、已销毁且清册归属、条目快照
// 与处理日期全部合法的档案，身份不一致时同样按整库损坏拒绝。
func TestOpenRejectsIdentityMismatchDespiteValidRest(t *testing.T) {
	frozen := `{"version":1,"archives":{"A-1":{"id":"A-2","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","freezes":[{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":false}]}},"manifests":{}}`
	destroyed := `{"version":1,"archives":{"A-1":{"id":"A-2","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","freezes":[],"destroyed":true,"manifest_id":"APP-1"}},"manifests":{"APP-1":{"application_id":"APP-1","processed_on":"2025-01-10","entries":[{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10"}]}}}`

	for name, state := range map[string]string{
		"被冻结档案 id 不一致":         frozen,
		"已销毁档案 id 不一致（清册完全合法）": destroyed,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, state)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("编号矛盾不应被合法的冻结或清册信息掩盖")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			if !strings.Contains(err.Error(), "A-1") || !strings.Contains(err.Error(), "A-2") {
				t.Fatalf("错误信息应同时给出查找编号与记录内编号，得到 %v", err)
			}
			if got := readStateFile(t, dir); got != state {
				t.Fatalf("打开失败后原文件被改动: %q -> %q", state, got)
			}
		})
	}
}

// 比较以 JSON 解码后的实际文本为准：转义写法不同但解码后相同的编号仍合法。
func TestOpenAcceptsEscapedEquivalentIdentity(t *testing.T) {
	dir := t.TempDir()
	// 键以 Unicode 转义写出 "A-1"，记录 id 直接写出，解码后是同一编号。
	writeStateFile(t, dir, `{"version":1,"archives":{"\u0041-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","freezes":[]}},"manifests":{}}`)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("解码后一致的编号应能打开: %v", err)
	}
	defer s.Close()
	h, found, err := s.History("A-1")
	if err != nil || !found {
		t.Fatalf("登记记录应能查询: found=%v err=%v", found, err)
	}
	if h.ID != "A-1" {
		t.Fatalf("历史编号应为 A-1，得到 %q", h.ID)
	}
}

// 保管库打开后保存内容才出现编号矛盾：下一次查询历史、取回清册、销毁前核对
// 或办理业务都必须按整库记录损坏失败——即使操作的是另一份正常档案；
// 不返回正常历史、清册或部分报告，也不改变档案状态或生成清册。
func TestIdentityCorruptionAfterOpenFailsSubsequentOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "B-2", "凭证", "2020-01-01", "2025-01-10")

	original := readStateFile(t, dir)
	if !strings.Contains(original, `"id": "A-1"`) {
		t.Fatalf("保存内容中应存在 A-1 的 id 字段: %q", original)
	}
	// 把 A-1 登记内容里的 id 改成 A-9，其余内容（含正常的 B-2）保持原样。
	corrupt := strings.Replace(original, `"id": "A-1"`, `"id": "A-9"`, 1)
	writeStateFile(t, dir, corrupt)

	if _, found, err := s.History("B-2"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("损坏后查询另一份正常档案也应按整库损坏失败: found=%v err=%v", found, err)
	}
	if _, found, err := s.GetManifest("APP-1"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("损坏后取回清册应按整库损坏失败: found=%v err=%v", found, err)
	}
	if _, err := s.Check(CheckRequest{
		ApplicationID: "APP-1",
		ProcessedOn:   MustParseDate("2025-01-10"),
		ArchiveIDs:    []string{"B-2"},
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后销毁前核对应按整库损坏失败: %v", err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1",
		ProcessedOn:   MustParseDate("2025-01-10"),
		ArchiveIDs:    []string{"B-2"},
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后办理销毁应按整库损坏失败: %v", err)
	}
	if err := s.Register(RegisterInput{
		ID: "C-3", Category: "合同",
		Start: MustParseDate("2020-01-01"), End: MustParseDate("2025-01-10"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后登记新档案应按整库损坏失败: %v", err)
	}
	if got := readStateFile(t, dir); got != corrupt {
		t.Fatalf("失败后保存内容被改动: %q -> %q", corrupt, got)
	}
}

// 正常登记时已有的输入去首尾空白行为继续保留：登记保存的集合键与 id
// 是同一个规范化后的编号，重新打开与查询都不受影响。
func TestRegisterStillNormalizesIdentity(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg(t, s, "  A-1  ", "合同", "2020-01-01", "2025-01-10")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("去空白登记保存的记录应能重新打开: %v", err)
	}
	defer s2.Close()
	h, found, err := s2.History("A-1")
	if err != nil || !found {
		t.Fatalf("规范化后的编号应能查询: found=%v err=%v", found, err)
	}
	if h.ID != "A-1" {
		t.Fatalf("历史编号应为去空白后的 A-1，得到 %q", h.ID)
	}
}
