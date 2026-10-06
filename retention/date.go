package retention

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Date 是按日历日期（年月日）比较的日期，与时区和夏令时无关。
//
// 对外的文本格式统一为 YYYY-MM-DD。零值表示无效日期，不能用于业务。
type Date struct {
	year  int
	month int
	day   int
}

// ParseDate 按 YYYY-MM-DD 解析日期。
// 只接受完整的四位年份、两位月份、两位日期，且日期必须真实存在。
// 每一位都必须是 0-9 的数字：带正负号、缺少补零、首尾空白、
// 附带时间或含反斜杠转义的文本一律拒绝，不做去空白、补位或去符号。
func ParseDate(s string) (Date, error) {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return Date{}, &InvalidDateError{Value: s}
	}
	year, ok := parseFixedDigits(s[0:4])
	if !ok {
		return Date{}, &InvalidDateError{Value: s}
	}
	month, ok := parseFixedDigits(s[5:7])
	if !ok {
		return Date{}, &InvalidDateError{Value: s}
	}
	day, ok := parseFixedDigits(s[8:10])
	if !ok {
		return Date{}, &InvalidDateError{Value: s}
	}
	if !validCalendarDate(year, month, day) {
		return Date{}, &InvalidDateError{Value: s}
	}
	return Date{year, month, day}, nil
}

// parseFixedDigits 把一段必须全部由 0-9 数字组成的文本转成整数。
// 任何非数字字符（含正负号、空白、反斜杠）都报告失败。
func parseFixedDigits(s string) (int, bool) {
	value := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		value = value*10 + int(c-'0')
	}
	return value, true
}

// MustParseDate 按 YYYY-MM-DD 解析日期，失败即 panic。
// 只适合在测试或字面量构造中使用。
func MustParseDate(s string) Date {
	d, err := ParseDate(s)
	if err != nil {
		panic(err)
	}
	return d
}

// String 返回 YYYY-MM-DD 形式的文本。
func (d Date) String() string {
	var b strings.Builder
	b.Grow(10)
	writePadded(&b, d.year, 4)
	b.WriteByte('-')
	writePadded(&b, d.month, 2)
	b.WriteByte('-')
	writePadded(&b, d.day, 2)
	return b.String()
}

// IsZero 报告日期是否为零值（未设置）。
func (d Date) IsZero() bool { return d.year == 0 && d.month == 0 && d.day == 0 }

// Before 按日历日期报告 d 是否早于 other。
func (d Date) Before(other Date) bool {
	if d.year != other.year {
		return d.year < other.year
	}
	if d.month != other.month {
		return d.month < other.month
	}
	return d.day < other.day
}

// After 按日历报告 d 是否晚于 other。
func (d Date) After(other Date) bool { return other.Before(d) }

// Equal 按日历报告两个日期是否相同。
func (d Date) Equal(other Date) bool {
	return d.year == other.year && d.month == other.month && d.day == other.day
}

// MarshalJSON 按 YYYY-MM-DD 序列化。
func (d Date) MarshalJSON() ([]byte, error) {
	return []byte(`"` + d.String() + `"`), nil
}

// UnmarshalJSON 按 YYYY-MM-DD 形式反序列化并校验真实日期。
// 先按 JSON 字符串规则解码（普通字符与 \uXXXX 等合法转义混用时
// 以解码后的实际文本为准），再按 ParseDate 校验；数字、对象等
// 非字符串值以及解码后仍不合法的文本都拒绝。解码失败时保留原日期。
func (d *Date) UnmarshalJSON(data []byte) error {
	if len(data) < 2 || data[0] != '"' || data[len(data)-1] != '"' {
		return &InvalidDateError{Value: string(data)}
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return &InvalidDateError{Value: string(data)}
	}
	parsed, err := ParseDate(text)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

func writePadded(b *strings.Builder, value, width int) {
	s := strconv.Itoa(value)
	for i := len(s); i < width; i++ {
		b.WriteByte('0')
	}
	b.WriteString(s)
}

func validCalendarDate(year, month, day int) bool {
	// 年份允许 0001..9999；月份、日期必须真实存在，含闰年规则。
	if year < 1 || year > 9999 || month < 1 || month > 12 || day < 1 {
		return false
	}
	daysInMonth := 31
	switch month {
	case 4, 6, 9, 11:
		daysInMonth = 30
	case 2:
		leap := year%4 == 0 && (year%100 != 0 || year%400 == 0)
		if leap {
			daysInMonth = 29
		} else {
			daysInMonth = 28
		}
	}
	return day <= daysInMonth
}
