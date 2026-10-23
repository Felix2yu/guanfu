package sources

import (
	"encoding/json"
	"testing"
)

// TestFlexIDShapes 上游主键可能是字符串（现网 mujian 的 records[].id）也可能是数字，
// 两种都要解得开；超过 int53 的雪花 id 必须原样保留，不允许位宽/精度损失。
func TestFlexIDShapes(t *testing.T) {
	cases := []struct{ body, want string }{
		{`"77"`, "77"},
		{`77`, "77"},
		{`"18446744073709551615"`, "18446744073709551615"},
		{`18446744073709551615`, "18446744073709551615"},
		{`"0077"`, "0077"}, // 不做数字归一化：上游给什么就是什么
		{`""`, ""},
		{`null`, ""},
	}
	for _, c := range cases {
		var id flexID
		if err := json.Unmarshal([]byte(c.body), &id); err != nil {
			t.Fatalf("%s 解析失败: %v", c.body, err)
		}
		if id.String() != c.want {
			t.Fatalf("%s → %q, want %q", c.body, id.String(), c.want)
		}
	}
}

// TestFlexIDAbsentKeepsValue 增量响应不带该字段时保留已有值，不清空。
func TestFlexIDAbsentKeepsValue(t *testing.T) {
	v := struct {
		ID flexID `json:"id"`
	}{ID: "keep"}
	if err := json.Unmarshal([]byte(`{}`), &v); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if v.ID != "keep" {
		t.Fatalf("缺字段清了已有值: %q", v.ID)
	}
}
