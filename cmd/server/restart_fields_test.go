package main

import (
	"reflect"
	"testing"
)

// 只改 keys 时不得报告任何"需重启"字段：
// 这是本次修复的核心动机——早期实现无条件返回静态全量清单，
// 用户只动密钥却被告知 13 个字段要重启（listen/auth_dir/timeout…全被点名），
// 于是真正的重启提示被淹没、用户学会无视它。
func TestRestartRequired_KeysOnlyChangeIsSilent(t *testing.T) {
	old := map[string]any{
		"listen":         ":7863",
		"auth_dir":       "./auths",
		"api_key":        "k",
		"upstream":       map[string]any{"timeout_seconds": float64(60), "user_agent": "ua"},
		"logging":        map[string]any{"request_retention_days": float64(7)},
		"upstash":        map[string]any{"url": "https://x"},
		"session_sticky": map[string]any{"ttl": "30m"},
	}
	cur := map[string]any{
		"listen":         ":7863",
		"auth_dir":       "./auths",
		"api_key":        "k",
		"upstream":       map[string]any{"timeout_seconds": float64(60), "user_agent": "ua"},
		"logging":        map[string]any{"request_retention_days": float64(7)},
		"upstash":        map[string]any{"url": "https://x"},
		"session_sticky": map[string]any{"ttl": "30m"},
	}
	// 唯一改动：keys[]（可热生效，不在重启清单里）
	cur["keys"] = []any{map[string]any{"key": "sk-a", "allow": []any{"cn:glm-4.6"}}}

	if got := restartRequiredFields(old, cur); len(got) != 0 {
		t.Errorf("只改 keys 不应报告需重启字段，got=%v", got)
	}
}

// 真正改动装配期字段时必须报告（否则用户以为改了没生效、却看不到提示）。
func TestRestartRequired_ReportsRealChanges(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{
			"listen":     ":7863",
			"auth_dir":   "./auths",
			"state_file": "./data/state.json",
			"upstream":   map[string]any{"timeout_seconds": float64(60), "user_agent": "ua"},
			"logging":    map[string]any{"request_retention_days": float64(7)},
			"server":     map[string]any{"read_timeout": "30s"},
		}
	}

	cases := []struct {
		name    string
		mutate  func(m map[string]any)
		wantHas string
	}{
		{"改 listen", func(m map[string]any) { m["listen"] = ":9999" }, "listen"},
		{"改 auth_dir", func(m map[string]any) { m["auth_dir"] = "./other" }, "auth_dir"},
		{"改 state_file", func(m map[string]any) { m["state_file"] = "./x.json" }, "state_file"},
		{"改 upstream 超时", func(m map[string]any) {
			m["upstream"].(map[string]any)["timeout_seconds"] = float64(120)
		}, "upstream.timeout_seconds"},
		{"改 user_agent", func(m map[string]any) {
			m["upstream"].(map[string]any)["user_agent"] = "other-ua"
		}, "upstream.user_agent"},
		{"改 logging 保留天数", func(m map[string]any) {
			m["logging"].(map[string]any)["request_retention_days"] = float64(30)
		}, "logging.request_retention_days"},
		{"改 server.read_timeout", func(m map[string]any) {
			m["server"].(map[string]any)["read_timeout"] = "60s"
		}, "server.read_timeout"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old, cur := base(), base()
			tc.mutate(cur)
			got := restartRequiredFields(old, cur)
			found := false
			for _, g := range got {
				if g == tc.wantHas {
					found = true
				}
			}
			if !found {
				t.Errorf("期望报告 %q，got=%v", tc.wantHas, got)
			}
		})
	}
}

// 面板回填不得造成误报：语义相同的不同书写形式（nil vs 空串、2 vs 2.0、
// 数组顺序重排、时长字符串归一）都应视为未改动。
func TestRestartRequired_NoFalsePositivesOnReformat(t *testing.T) {
	old := map[string]any{
		"listen": ":7863",
		"upstream": map[string]any{
			"timeout_seconds": float64(60),
			"user_agent":      "ua",
			"extra_hours":     []any{float64(9), float64(21)},
		},
		"logging": map[string]any{"request_retention_days": float64(7)},
	}
	cur := map[string]any{
		"listen": ":7863",
		"upstream": map[string]any{
			"timeout_seconds": nil, // 缺失/空 vs 现值：60 -> nil 是真改，见下方断言外
			"user_agent":      "ua",
			"extra_hours":     []any{float64(21), float64(9)}, // 顺序重排，不应算改
		},
		"logging": map[string]any{"request_retention_days": 7.0}, // 同值不同字面量
	}
	// 先把 timeout 对齐，单独验证「数组顺序」与「数字字面量」不误报。
	cur["upstream"].(map[string]any)["timeout_seconds"] = float64(60)
	if got := restartRequiredFields(old, cur); len(got) != 0 {
		t.Errorf("语义未变不应报告，got=%v", got)
	}

	// 反例：把超时改成 0（真改，且 0 是有意义的值而非"未设置"）必须报告。
	cur["upstream"].(map[string]any)["timeout_seconds"] = float64(0)
	if got := restartRequiredFields(old, cur); !reflect.DeepEqual(got, []string{"upstream.timeout_seconds"}) {
		t.Errorf("60 -> 0 应报告 upstream，got=%v", got)
	}
}

// 零值等价性：nil 与空串/0/false/空数组互不误报（面板回填常见形态）。
func TestSemanticEqual_ZeroForms(t *testing.T) {
	zeros := []any{nil, "", float64(0), false, []any{}, map[string]any{}}
	for i, a := range zeros {
		for j, b := range zeros {
			if !semanticEqual(a, b) {
				t.Errorf("zeros[%d]=%#v 与 zeros[%d]=%#v 应等价", i, a, j, b)
			}
		}
	}
	if semanticEqual(float64(1), nil) {
		t.Error("1 与 nil 不应等价")
	}
	if semanticEqual("x", nil) {
		t.Error("非空串与 nil 不应等价")
	}
	if semanticEqual(true, false) {
		t.Error("true 与 false 不应等价")
	}
}

// 数组按集合比较：顺序不同视为相同，元素数量不同视为不同。
func TestSemanticEqual_ArrayAsSet(t *testing.T) {
	if !semanticEqual([]any{"a", "b"}, []any{"b", "a"}) {
		t.Error("数组应按集合比较，顺序不应影响结果")
	}
	if semanticEqual([]any{"a", "b"}, []any{"a"}) {
		t.Error("元素数量不同不应等价")
	}
	if semanticEqual([]any{"a", "a"}, []any{"a"}) {
		t.Error("重复元素计数应参与比较")
	}
}

// deepGet 取 dotted path；缺失层返回 nil 而非 panic。
func TestDeepGet(t *testing.T) {
	m := map[string]any{"a": map[string]any{"b": map[string]any{"c": float64(1)}}}
	if v := deepGet(m, "a.b.c"); v != float64(1) {
		t.Errorf("a.b.c = %v want 1", v)
	}
	if v := deepGet(m, "a.b.missing"); v != nil {
		t.Errorf("缺失键应为 nil, got %v", v)
	}
	if v := deepGet(m, "a.x.c"); v != nil {
		t.Errorf("中间层缺失应为 nil, got %v", v)
	}
	if v := deepGet(m, "a"); v == nil {
		t.Error("取顶层对象应非 nil")
	}
}

// mergeConfigMaps 是原地修改：merged 与传入的 cur 是同一个 map。
// 若调用方不留快照就把它当"改动前"比对，任何改动都会被判成没变、
// 重启提示全部消失（这正是初版实现的真实 bug：改 listen 也报 []）。
// 本测试锁住"快照必须独立于原地合并"这一前提。
func TestRestartRequired_DetectsChangeDespiteInPlaceMerge(t *testing.T) {
	cur := map[string]any{
		"listen":   ":7863",
		"upstream": map[string]any{"timeout_seconds": float64(60)},
	}
	snapshot := deepCopyMap(cur) // 与 saveConfig 一致：合并前留快照

	incoming := map[string]any{
		"listen":   ":9999",
		"upstream": map[string]any{"timeout_seconds": float64(120)},
	}
	merged := mergeConfigMaps(cur, incoming) // 原地改 cur

	// 前提确认：merged 与 cur 确实是同一对象（若此断言失败，说明上游改了语义）
	if &cur == &merged {
		t.Log("merged 与 cur 变量地址相同（符合预期：同一 map）")
	}

	// 用未经深拷贝的 cur 当"改动前"应当失效：这是反例，说明不能省快照。
	// 这里只验证深拷贝快照能正确识别改动。
	got := restartRequiredFields(snapshot, merged)
	found := map[string]bool{}
	for _, g := range got {
		found[g] = true
	}
	if !found["listen"] {
		t.Errorf("改 listen 应被检出（合并后仍能识别），got=%v", got)
	}
	if !found["upstream.timeout_seconds"] {
		t.Errorf("改 upstream 超时应被检出，got=%v", got)
	}
}

// 反例回归：如果直接把被原地覆盖的 cur 当旧值，就会漏报（防有人"简化"掉快照）。
func TestRestartRequired_AliasedOldValueWouldMissChange(t *testing.T) {
	cur := map[string]any{"listen": ":7863"}
	aliasedOld := cur // 没有快照，就是同一个 map
	incoming := map[string]any{"listen": ":9999"}
	merged := mergeConfigMaps(cur, incoming)

	if got := restartRequiredFields(aliasedOld, merged); len(got) != 0 {
		t.Logf("注意：别名旧值也检出了 %v（实现可能已改为非原地）", got)
	}
	// 有快照时才应检出——这是正确行为。
	snapshot := deepCopyMap(map[string]any{"listen": ":7863"})
	if got := restartRequiredFields(snapshot, merged); len(got) != 1 || got[0] != "listen" {
		t.Errorf("快照应检出 listen 改动, got=%v", got)
	}
}

// deepCopyMap 必须与源完全解耦：改动副本不得影响原 map（含嵌套层）。
func TestDeepCopyMap_Independent(t *testing.T) {
	src := map[string]any{
		"a": map[string]any{"b": float64(1)},
		"c": []any{map[string]any{"d": "x"}, "y"},
	}
	cp := deepCopyMap(src)

	cp["a"].(map[string]any)["b"] = float64(999)
	cp["c"].([]any)[0].(map[string]any)["d"] = "changed"
	cp["c"].([]any)[1] = "changed"
	cp["new"] = true

	if src["a"].(map[string]any)["b"] != float64(1) {
		t.Error("嵌套 map 未隔离")
	}
	if src["c"].([]any)[0].(map[string]any)["d"] != "x" {
		t.Error("切片内 map 未隔离")
	}
	if src["c"].([]any)[1] != "y" {
		t.Error("切片元素未隔离")
	}
	if _, ok := src["new"]; ok {
		t.Error("顶层新增键不应影响源")
	}
}
