package sources

import "encoding/json"

// flexID 各源主键的容错形态。上游会把数值主键序列化成字符串（mujian 2026-10-23
// 现网同步报错实证：records[].id 变成 "id":"…"），数字与字符串两种都得吃下——
// 一个字段解不开，整批同步在第一条记录上就失败。
//
// 统一以字符串承载：ExternalID、请求路径、页面链接直接用它，上游换成雪花 id 也不存在
// 位宽与前端 JS 精度损失。解码失败的字段一律是「上游契约变了」，不做数字归一化。
type flexID string

func (f *flexID) UnmarshalJSON(data []byte) error {
	switch {
	case string(data) == "null":
		return nil
	case len(data) > 0 && data[0] == '"':
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*f = flexID(s)
	default:
		*f = flexID(data) // 数字字面量原样保留
	}
	return nil
}

func (f flexID) String() string { return string(f) }
