package retention

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// 下列辅助量给出 JSON 源码中的 Unicode 转义文本：Go 解释字符串里写
// "\\u0032"，实际 Go 字符串值就是六个字符的 2。
var (
	u2     = "\\u0032"
	u0     = "\\u0030"
	u5     = "\\u0035"
	uDash  = "\\u002d"
	uPlus  = "\\u002b"
	uSpace = "\\u0020"
)

func TestParseDateAndCompare(t *testing.T) {
	d, err := ParseDate("2024-02-29") // 2024 是闰年
	if err != nil {
		t.Fatalf("闰年日期应可解析: %v", err)
	}
	if d.String() != "2024-02-29" {
		t.Fatalf("往返格式不一致: %s", d.String())
	}

	if _, err := ParseDate("2023-02-29"); err == nil {
		t.Fatal("2023-02-29 不存在，应失败")
	}
	for _, bad := range []string{
		"", "2024-1-1", "2024/01/01", "2024-13-01", "2024-00-10",
		"2024-04-31", "2024-12-32", "abcd-ef-gh", " 2024-01-01", "2024-01-01 ",
	} {
		if _, err := ParseDate(bad); err == nil {
			t.Fatalf("%q 不是合法 YYYY-MM-DD，应失败", bad)
		}
	}

	a := MustParseDate("2024-03-31")
	b := MustParseDate("2024-04-01")
	if !a.Before(b) || b.Before(a) || a.Equal(b) {
		t.Fatal("跨月日历日期比较错误")
	}
	// 日历日期比较不经过任何时区，同一字符串两次解析必须完全相等。
	if !MustParseDate("2024-08-01").Equal(MustParseDate("2024-08-01")) {
		t.Fatal("相同日期不相等")
	}
	if (Date{}).IsZero() != true {
		t.Fatal("零值日期应报告 IsZero")
	}
}

// ParseDate 逐字校验 YYYY-MM-DD：年、月、日分别恰好四位、两位、两位半角
// 数字，分隔符必须是半角横线。strconv.Atoi 曾接受带正负号的数字段，使
// “2025-+1-10”“2025-01-+1”被当成普通日期；这些写法连同缺少补零、首尾
// 空白、附带时间与直接写出的反斜杠转义都必须明确失败，不能靠去空白、补位、
// 去符号或解释转义接受。
func TestParseDateRejectsNonCanonicalText(t *testing.T) {
	bad := []string{
		"2025-+1-10",           // 月份带正号：曾经被 Atoi 当成 1 月
		"2025-01-+1",           // 日期带正号：曾经被 Atoi 当成 1 日
		"2025--1-10",           // 月份带负号
		"2025-01--1",           // 日期带负号
		"+025-01-10",           // 年份带正号
		"-025-01-10",           // 年份带负号
		" 2025-01-10",          // 首部空白
		"2025-01-10 ",          // 尾部空白
		"2025 -01-10",          // 年份中夹空白
		"2025-1-10",            // 月份缺少补零
		"2025-01-1",            // 日期缺少补零
		"25-01-10",             // 年份缺少补零
		"02025-01-10",          // 年份五位
		"2025-01-10T00:00:00Z", // 附带时间
		"2025-01-10 00:00",     // 空格附带时间
		"2025/01/10",           // 非半角横线分隔符
		"2025．01．10",           // 非 ASCII 全角句点
		// 直接传给 ParseDate 的反斜杠转义文本只是普通字符串，绝不能被当作日期：
		"2025" + uDash + "01-10", // JSON 转义出的横线
		"2025-01-1" + u0,         // JSON 转义出的末位数字
	}
	for _, in := range bad {
		d, err := ParseDate(in)
		if err == nil {
			t.Fatalf("%q 必须解析失败，却得到 %s", in, d)
		}
		if !errors.Is(err, ErrInvalidDate) {
			t.Fatalf("%q 的错误应可由 ErrInvalidDate 识别，得到 %v", in, err)
		}
	}
}

// 年份范围 0001..9999，月份与日期必须真实存在，闰年沿用公历规则：
// 2000-02-29 有效，1900-02-29 无效。
func TestParseDateCalendarRules(t *testing.T) {
	good := []string{
		"0001-01-01", "9999-12-31", "2000-02-29", "2024-02-29",
		"2025-02-28", "2025-04-30",
	}
	for _, in := range good {
		d, err := ParseDate(in)
		if err != nil {
			t.Fatalf("%q 是真实存在的日期，应解析成功: %v", in, err)
		}
		if d.String() != in {
			t.Fatalf("%q 往返不一致: %s", in, d)
		}
	}
	bad := []string{
		"0000-01-01", "10000-01-01", "1900-02-29", "2023-02-29",
		"2025-02-30", "2025-00-10", "2025-13-01", "2025-06-31",
	}
	for _, in := range bad {
		if _, err := ParseDate(in); !errors.Is(err, ErrInvalidDate) {
			t.Fatalf("%q 不是真实日期，应返回 ErrInvalidDate，得到 %v", in, err)
		}
	}
}

// 读取 JSON 日期字符串时，以字符串所表示的实际文本判断合法性：普通字符与
// Unicode 转义可以混用，解码后同一文本必须得到同一个日期；转义后出现加号、
// 空白等非法字符仍要拒绝。数字、对象、布尔、null 等非字符串值一律拒绝；
// 给已有日期解码非法值时必须失败并保留原日期。
func TestDateUnmarshalJSONEscapesAndRejections(t *testing.T) {
	want := MustParseDate("2025-01-10")
	good := []string{
		`"2025-01-10"`,                         // 普通写法
		`"` + u2 + `025-01-10"`,                // 首个数字用 Unicode 转义
		`"2025-01-1` + u0 + `"`,                // 末位数字用 Unicode 转义
		`"` + u2 + u0 + u2 + u5 + `-01-10"`,    // 年份全部转义
		`"2025` + uDash + `01` + uDash + `10"`, // 两处分隔横线也用转义
		`"` + u2 + `025` + uDash + `01-10"`,    // 普通字符与转义混用
	}
	for _, raw := range good {
		var d Date
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			t.Fatalf("%s 解码后是合法日期，不应失败: %v", raw, err)
		}
		if !d.Equal(want) {
			t.Fatalf("%s 应解析为 %s，得到 %s", raw, want, d)
		}
		if d.String() != "2025-01-10" {
			t.Fatalf("%s 的输出仍应为规范写法 2025-01-10，得到 %s", raw, d)
		}
	}

	bad := []string{
		`"2025-+1-10"`,                    // 明文加号
		`"2025` + uDash + uPlus + `1-10"`, // 月份加号用 Unicode 转义
		`"2025-01-` + uPlus + `1"`,        // 日期加号用 Unicode 转义
		`" 2025-01-10"`,                   // 明文首部空白
		`"` + uSpace + `2025-01-10"`,      // 转义后首部是空白
		`"2025-01-10 "`,                   // 明文尾部空白
		`"2025-01-10` + uSpace + `"`,      // 转义后尾部是空白
		`"2025-1-10"`,                     // 缺少补零
		`20250110`,                        // 数字不是字符串
		`123`,
		`true`,              // 布尔
		`{"year":2025}`,     // 对象
		`["2025-01-10"]`,    // 数组
		`null`,              // null 不是日期
		`"2025-01-10extra"`, // 附带多余文本
		`"2025-02-29"`,      // 2025 不是闰年
		`"1900-02-29"`,      // 1900 不是闰年
	}
	for _, raw := range bad {
		var d Date
		if err := json.Unmarshal([]byte(raw), &d); err == nil {
			t.Fatalf("%s 不应能解码为日期，却得到 %s", raw, d)
		} else if !errors.Is(err, ErrInvalidDate) {
			t.Fatalf("%s 的错误应可由 ErrInvalidDate 识别，得到 %v", raw, err)
		}
	}

	// 给已有日期解码非法值：失败且原日期保持不变。
	original := MustParseDate("2024-06-01")
	d := original
	for _, raw := range []string{
		`"2025-+1-10"`,
		`"2025` + uDash + uPlus + `1-10"`,
		`"2025-01-` + uPlus + `1"`,
		`"not-a-date"`, `20250110`, `null`,
	} {
		if err := json.Unmarshal([]byte(raw), &d); err == nil {
			t.Fatalf("%s 解码应失败", raw)
		}
		if !d.Equal(original) {
			t.Fatalf("%s 解码失败后原日期应保留，得到 %s", raw, d)
		}
	}

	// 合法转义值覆盖原日期。
	if err := json.Unmarshal([]byte(`"`+u2+`025-01-10"`), &d); err != nil {
		t.Fatalf("合法转义日期应能覆盖: %v", err)
	}
	if !d.Equal(want) {
		t.Fatalf("覆盖后日期不符: %s", d)
	}

	// 序列化始终输出规范的普通写法，不产生转义差异。
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `"2025-01-10"` {
		t.Fatalf("序列化应为普通写法，得到 %s", out)
	}
}

// 可缺省、可为 null 的日期字段（*Date）：字段缺失与显式 null 都保持 nil，
// 一旦给出字符串就必须是真实日期——null 不会顶替成零值，非法字符串也不会
// 被当作缺省。
func TestOptionalDatePointerJSONSemantics(t *testing.T) {
	type holder struct {
		On *Date `json:"on,omitempty"`
	}

	for _, raw := range []string{`{}`, `{"on":null}`} {
		var h holder
		if err := json.Unmarshal([]byte(raw), &h); err != nil {
			t.Fatalf("%s 应解码成功: %v", raw, err)
		}
		if h.On != nil {
			t.Fatalf("%s 缺省/null 应保持 nil，得到 %v", raw, *h.On)
		}
	}

	var h holder
	if err := json.Unmarshal([]byte(`{"on":"`+u2+`025-01-10"}`), &h); err != nil {
		t.Fatalf("合法转义日期应解码成功: %v", err)
	}
	if h.On == nil || !h.On.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("指针日期解码错误: %+v", h.On)
	}

	for _, raw := range []string{
		fmt.Sprintf(`{"on":"2025%s%s1-10"}`, uDash, uPlus),
		`{"on":"2025-01-` + uPlus + `1"}`,
		`{"on":20250110}`,
	} {
		var bad holder
		if err := json.Unmarshal([]byte(raw), &bad); !errors.Is(err, ErrInvalidDate) {
			t.Fatalf("%s 应返回 ErrInvalidDate，得到 %v", raw, err)
		}
	}
}
