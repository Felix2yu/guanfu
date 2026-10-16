package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"time"

	"github.com/Felix2yu/guanfu/internal/model"
)

// exportDateRe 导出参数只认 YYYY-MM-DD：导出是给"数据要搬家"用的，
// 静默收下一个错误格式、产出一个空文件，比直接报错恶劣得多。
var exportDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// validExportDate 光过正则不够：2026-13-45 形态合法却不存在，
// 那样只会导出一个空文件，调用方以为"这段时间确实没数据"。
func validExportDate(v string) bool {
	if !exportDateRe.MatchString(v) {
		return false
	}
	_, err := time.ParseInLocation("2006-01-02", v, model.LocalLocation())
	return err == nil
}

// 导出：观复自己的数据出口（规划 §4.3，防锁定）。
//
//	GET /api/v1/export?from=YYYY-MM-DD&to=YYYY-MM-DD&format=json
//
// 两条口径说明：
//   - from/to 可省略，省略即全库范围（用 events 的实际 min/max 填入 meta，便于复核）；
//   - 逐条流式编码，不在内存里攒全量；因此一旦开始输出就无法再改状态码，
//     中途出错只能截断 + 记日志（参数校验全部在首个字节之前完成）。
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if format := q.Get("format"); format != "" && format != "json" {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("暂不支持 format=%q，当前只提供 json", format))
		return
	}
	from, to := q.Get("from"), q.Get("to")
	for name, v := range map[string]string{"from": from, "to": to} {
		if v != "" && !validExportDate(v) {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("参数 %s 需是有效日期（YYYY-MM-DD），如 2026-10-01（收到 %q）", name, v))
			return
		}
	}
	if from != "" && to != "" && from > to {
		from, to = to, from
	}

	// 省略范围时回填实际边界，并顺带拿到精确条数（走 local_date 索引，成本一次 COUNT）。
	min, max, err := s.store.DateRange()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询数据范围失败: "+err.Error())
		return
	}
	effectiveFrom, effectiveTo := from, to
	if effectiveFrom == "" {
		effectiveFrom = min
	}
	if effectiveTo == "" {
		effectiveTo = max
	}
	count := 0
	if effectiveFrom != "" && effectiveTo != "" {
		counts, err := s.store.CountsByRange(effectiveFrom, effectiveTo)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "统计导出条数失败: "+err.Error())
			return
		}
		for _, n := range counts {
			count += n
		}
	}

	filename := "guanfu-export.json"
	if effectiveFrom != "" || effectiveTo != "" {
		filename = fmt.Sprintf("guanfu-export-%s_%s.json", orUnknown(effectiveFrom), orUnknown(effectiveTo))
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Cache-Control", "no-store") // 导出随数据变，不能让中间层缓存
	w.WriteHeader(http.StatusOK)

	enc := json.NewEncoder(w)
	if _, err := io.WriteString(w, `{"meta":`); err != nil {
		return
	}
	meta := exportMeta{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Timezone:    model.LocalLocation().String(),
		From:        effectiveFrom,
		To:          effectiveTo,
		Count:       count,
		AmountUnit:  "cents",
		Note:        "media[].id 是各源侧的引用（mujian/qingye 为 uploads 键、immich 为资产 UUID、bili-history 为封面外链），换实例后按源重取",
	}
	if err := enc.Encode(meta); err != nil {
		log.Printf("[export] 写 meta 失败: %v", err)
		return
	}
	if _, err := io.WriteString(w, `,"events":[`); err != nil {
		return
	}

	written := 0
	err = s.store.ExportEvents(from, to, func(e *model.Event) error {
		if written > 0 {
			if _, err := io.WriteString(w, ","); err != nil {
				return err
			}
		}
		// 代理路径只在观复自己跑着时有意义，导出里剥掉，保留源侧引用。
		for i := range e.Media {
			e.Media[i].ProxyPath = ""
		}
		if err := enc.Encode(e); err != nil {
			return err
		}
		written++
		return nil
	})
	if _, err := io.WriteString(w, "]}"); err != nil {
		return
	}
	if err != nil {
		// 状态码已发出，只能记日志；末尾的 `]}` 让残缺文件仍可被解析定位。
		log.Printf("[export] 导出中断（已写出 %d/%d 条）: %v", written, count, err)
		return
	}
	if written != count {
		// 条数用 COUNT 预取、事件用游标流读，两者之间若有同步写入会短暂不一致；
		// 如实记下来，不假装数字永远对得上。
		log.Printf("[export] meta.count=%d 与实际写出 %d 条不一致（导出期间有同步写入）", count, written)
	}
	log.Printf("[export] %s..%s 导出 %d 条", effectiveFrom, effectiveTo, written)
}

func orUnknown(s string) string {
	if s == "" {
		return "all"
	}
	return s
}

type exportMeta struct {
	GeneratedAt string `json:"generated_at"`
	Timezone    string `json:"timezone"`
	From        string `json:"from"`
	To          string `json:"to"`
	Count       int    `json:"count"`
	AmountUnit  string `json:"amount_unit"`
	Note        string `json:"note"`
}
