package retention

import "testing"

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
