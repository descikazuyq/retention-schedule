package retention

import (
	"encoding/json"
	"fmt"
	"time"
)

// Date 是公历日期，格式固定为 YYYY-MM-DD。
//
// 内部以 UTC 零点保存，比较一律按日历日期进行，
// 不受调用方所在时区或夏令时影响。
type Date struct {
	time.Time
}

const dateLayout = "2006-01-02"

// ParseDate 严格解析 YYYY-MM-DD 格式的真实日历日期。
// 格式不符或日期不存在（如 2024-02-30、2024-13-01）都会失败。
func ParseDate(s string) (Date, error) {
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return Date{}, fmt.Errorf("%w: 日期格式应为 YYYY-MM-DD: %q", ErrInvalidDate, s)
	}
	// time.Parse 会把 2024-02-30 规范成 2024-03-01，
	// 格式化回来与原串比较即可识别不存在的日期。
	if t.Format(dateLayout) != s {
		return Date{}, fmt.Errorf("%w: 日期不存在: %q", ErrInvalidDate, s)
	}
	return Date{t}, nil
}

// String 返回 YYYY-MM-DD 形式。
func (d Date) String() string { return d.Format(dateLayout) }

// MarshalJSON 把日期序列化为 "YYYY-MM-DD" 字符串。
func (d Date) MarshalJSON() ([]byte, error) {
	return []byte(`"` + d.Format(dateLayout) + `"`), nil
}

// UnmarshalJSON 解析 "YYYY-MM-DD" 字符串。
func (d *Date) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return fmt.Errorf("%w: 日期格式应为 YYYY-MM-DD: %q", ErrInvalidDate, s)
	}
	if t.Format(dateLayout) != s {
		return fmt.Errorf("%w: 日期不存在: %q", ErrInvalidDate, s)
	}
	d.Time = t
	return nil
}
