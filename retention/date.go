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
//
// 逐字要求：年、月、日分别恰好是四位、两位、两位的数字，数字只允许半角
// 0-9；两处分隔符必须是半角横线 '-'。带正负号、缺少补零、首尾空白、
// 附带时间，或在文本里直接写出反斜杠转义（如 `\u0032025-01-10`）都
// 明确失败——绝不通过去空白、补位、去掉符号或解释转义来接受。JSON 字符串
// 的转义解码在 UnmarshalJSON 中处理，不发生在这里。
func ParseDate(s string) (Date, error) {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return Date{}, &InvalidDateError{Value: s}
	}
	// 不用 strconv.Atoi：它会接受 "+1"、"-1" 这类带符号文本，使
	// "2025-+1-10"、"2025-01-+1" 被当成普通日期。逐字符只认 0-9。
	year, ok1 := parseDigits(s[0:4])
	month, ok2 := parseDigits(s[5:7])
	day, ok3 := parseDigits(s[8:10])
	if !ok1 || !ok2 || !ok3 {
		return Date{}, &InvalidDateError{Value: s}
	}
	if !validCalendarDate(year, month, day) {
		return Date{}, &InvalidDateError{Value: s}
	}
	return Date{year, month, day}, nil
}

// parseDigits 把一段只含半角数字 0-9 的文本解析成整数；出现任何非数字
// 字符（正负号、空白、字母、反斜杠等）即返回 ok=false。
func parseDigits(s string) (int, bool) {
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
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
//
// 以 JSON 字符串解码后所表示的实际文本判断合法性：先让 encoding/json
// 完成字符串解码，普通字符与 \uXXXX 等合法 JSON 转义可以混用——
// "\u0032025-01-10" 与 "2025-01-10" 表示同一个日期，解析结果与输出
// 都为 2025-01-10；转义后出现加号、空白等非法字符仍按实际文本拒绝。
//
// 数字、对象、布尔或 null 等非字符串 JSON 值一律拒绝；解码失败时保留
// 调用前的原日期，不用零值顶替。
func (d *Date) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return &InvalidDateError{Value: string(data)}
	}
	parsed, err := ParseDate(s)
	if err != nil {
		// 返回原错误（仍是可由 ErrInvalidDate 识别的 InvalidDateError），
		// 且不写入 *d：给已有日期解码非法值时原日期保持不变。
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
