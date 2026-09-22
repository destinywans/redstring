package searchtext

import "testing"

func TestMatch(t *testing.T) {
	for _, tc := range []struct {
		value, query string
		want         bool
	}{
		{"资源管理", "", true},
		{"资源管理", "资源", true},
		{"资源管理", "ziyuan", true},
		{"资源管理", "ZYGL", true},
		{"资源管理", "zi yuan guan li", true},
		{"资源管理", "资源guanli", true},
		{"资源管理", "资源gl", true},
		{"资源管理", "zi源", true},
		{"Redis缓存", "REDIS hc", true},
		{"Redis缓存", "redis缓存", true},
		{"Redis缓存", "redis缓cun", true},
		{"自愿管理", "资源", false},
		{"自愿管理", "资源gl", false},
		{"资源管理", "missing", false},
		{"node_prod", "_", true},
		{"node-prod", "_", false},
		{"node-prod", "%", false},
		{"100%资源", "%ziyuan", true},
		{"女工", "nvgong", true},
		{"女工", "nügong", true},
		{"Redis Cache", "rediscache", true},
	} {
		if got := Match(tc.value, tc.query); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.value, tc.query, got, tc.want)
		}
	}
}
