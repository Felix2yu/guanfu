package store

import (
	"fmt"
	"sort"
	"strings"
)

// 聚合维度白名单：列标识符要拼进 GROUP BY（占位符只能用于值），
// 因此取值只允许来自这张表，调用方传什么都先查表。
var statGroupColumns = map[string]string{
	"source":     "source",
	"event_type": "event_type",
	"day":        "local_date",
	"month":      "substr(local_date, 1, 7)",
}

// StatGroupBys 返回受支持的聚合维度（MCP 工具的 enum 与错误提示共用，避免两处漂移）。
func StatGroupBys() []string {
	out := make([]string, 0, len(statGroupColumns))
	for k := range statGroupColumns {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// MoneyTotals 单一币种下的收支合计（单位：分）。
// 判定口径与时间轴顶栏一致：detail_json 里 type=="income" 记收入，其余带金额事件记支出。
type MoneyTotals struct {
	IncomeCents  int64 `json:"income_cents"`
	ExpenseCents int64 `json:"expense_cents"`
}

// StatRow 一个聚合分组。Totals 按币种分桶，不同币种不混加。
type StatRow struct {
	Key    string                 `json:"key"`
	Count  int                    `json:"count"`
	Totals map[string]MoneyTotals `json:"totals"`
}

// StatsResult 区间聚合结果。
type StatsResult struct {
	From       string                 `json:"from"`
	To         string                 `json:"to"`
	GroupBy    string                 `json:"group_by"`
	TotalCount int                    `json:"total_count"`
	Totals     map[string]MoneyTotals `json:"totals"`
	Rows       []StatRow              `json:"rows"`

	// 以下三项是给消费方（MCP / 前端）的口径说明与状态提示，由调用方按需填。
	AmountUnit string `json:"amount_unit,omitempty"`
	TotalsRule string `json:"totals_rule,omitempty"`
	Note       string `json:"note,omitempty"`
}

// StatFilter 聚合查询条件。From/To 为 local_date 闭区间，均必填。
type StatFilter struct {
	From      string
	To        string
	GroupBy   string // 空 = event_type
	Source    string
	EventType string
	Query     string // 标题/摘要字面量检索，与 ListEvents 同口径
}

// 收入判定表达式。detail_json 可能是空串或非法 JSON，先用 json_valid 洗成 {}
// 再取 $.type——SQLite 不保证 CASE/AND 的短路求值，直接 json_extract 会整条查询报错。
const incomeCond = `amount_cents > 0 AND
	COALESCE(json_extract(CASE WHEN json_valid(detail_json) THEN detail_json ELSE '{}' END, '$.type'), '') = 'income'`

// likeConds/likeArgs 关键词检索的 SQL 片段，ListEvents 与 Stats 共用：
// 否则「明细命中 1 条、total_count 报 4 条」这种自相矛盾的返回会直接坑到调用方。
func likeConds(query string) string {
	return `(title LIKE ? ESCAPE '\' OR summary LIKE ? ESCAPE '\')`
}

func likeArgs(query string) []any {
	like := "%" + likeEscaper.Replace(query) + "%"
	return []any{like, like}
}

// Stats 聚合 [From, To] 内的事件数与收支持。聚合下推 SQL，与 CountsByRange 同风格。
func (s *Store) Stats(f StatFilter) (*StatsResult, error) {
	if f.From == "" || f.To == "" {
		return nil, fmt.Errorf("from 与 to 均为必填（YYYY-MM-DD）")
	}
	groupBy := f.GroupBy
	if groupBy == "" {
		groupBy = "event_type"
	}
	col, ok := statGroupColumns[groupBy]
	if !ok {
		return nil, fmt.Errorf("不支持的聚合维度 %q（可选：%s）", groupBy, strings.Join(StatGroupBys(), " / "))
	}
	if f.From > f.To {
		f.From, f.To = f.To, f.From
	}

	// day/month 是时间序列，按日期升序读；其余按条数倒序，长尾分类沉底。
	order := "ORDER BY n DESC, k ASC"
	if groupBy == "day" || groupBy == "month" {
		order = "ORDER BY k ASC"
	}

	var (
		conds = []string{"local_date >= ?", "local_date <= ?"}
		args  = []any{f.From, f.To}
	)
	if f.Source != "" {
		conds = append(conds, "source = ?")
		args = append(args, f.Source)
	}
	if f.EventType != "" {
		conds = append(conds, "event_type = ?")
		args = append(args, f.EventType)
	}
	if f.Query != "" {
		conds = append(conds, likeConds(f.Query))
		args = append(args, likeArgs(f.Query)...)
	}

	rows, err := s.DB.Query(`SELECT `+col+` AS k, currency, COUNT(*) AS n,
			COALESCE(SUM(CASE WHEN `+incomeCond+` THEN amount_cents ELSE 0 END), 0) AS income,
			COALESCE(SUM(CASE WHEN amount_cents > 0 AND NOT (`+incomeCond+`) THEN amount_cents ELSE 0 END), 0) AS expense
		FROM events WHERE `+strings.Join(conds, " AND ")+`
		GROUP BY k, currency `+order, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var (
		byKey  = map[string]*StatRow{}
		keys   []string
		totals = map[string]MoneyTotals{}
		grand  int
	)
	for rows.Next() {
		var (
			k, currency    string
			n, income, exp int64
		)
		if err := rows.Scan(&k, &currency, &n, &income, &exp); err != nil {
			return nil, err
		}
		if currency == "" {
			// 无币种的历史行按主币种归集，不凭空造空键。
			currency = "CNY"
		}
		row, seen := byKey[k]
		if !seen {
			row = &StatRow{Key: k, Totals: map[string]MoneyTotals{}}
			byKey[k] = row
			keys = append(keys, k)
		}
		row.Count += int(n)
		t := row.Totals[currency]
		t.IncomeCents += income
		t.ExpenseCents += exp
		row.Totals[currency] = t

		g := totals[currency]
		g.IncomeCents += income
		g.ExpenseCents += exp
		totals[currency] = g
		grand += int(n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := &StatsResult{
		From:       f.From,
		To:         f.To,
		GroupBy:    groupBy,
		TotalCount: grand,
		Totals:     totals,
		Rows:       make([]StatRow, 0, len(keys)),
	}
	// SQL 已排好序，keys 记录的即首次出现顺序；同 key 多币种时保持该次序。
	for _, k := range keys {
		out.Rows = append(out.Rows, *byKey[k])
	}
	return out, nil
}
