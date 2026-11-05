package main

import "testing"

func TestGetUpdateChannel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", UpdateChannelNext},
		{"next", "next"},
		{"  NEXT ", "next"},
		{"stable", "stable"},
		{"Stable", "stable"},
		{"alpha", "alpha"},
		{"latest", "latest"},
		{"whatever", UpdateChannelNext},
	}
	for _, c := range cases {
		cfg := Config{UpdateChannel: c.in}
		if got := cfg.GetUpdateChannel(); got != c.want {
			t.Fatalf("GetUpdateChannel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestResolveTargetVersionByChannel(t *testing.T) {
	info := &npmPackageInfo{
		DistTags: map[string]string{
			"latest": "0.2.1-rc.5",
			"next":   "0.2.2-rc.1",
			"alpha":  "0.2.2-alpha.3",
		},
		Time: map[string]string{
			"created":       "2026-01-01T00:00:00Z",
			"modified":      "2026-01-02T00:00:00Z",
			"0.2.1-rc.5":    "2026-01-01T00:00:00Z",
			"0.2.2-alpha.3": "2026-01-02T00:00:00Z",
			"0.2.2-rc.1":    "2026-01-03T00:00:00Z",
		},
	}

	cases := []struct{ channel, want string }{
		{"next", "0.2.2-rc.1"},
		{"stable", "0.2.1-rc.5"},
		{"alpha", "0.2.2-alpha.3"},
		{"latest", "0.2.2-rc.1"},
	}
	for _, c := range cases {
		if got := resolveTargetVersion(info, c.channel); got != c.want {
			t.Fatalf("resolveTargetVersion(%q) = %q, want %q", c.channel, got, c.want)
		}
	}
}

func TestResolveTargetVersionFallsBackToMax(t *testing.T) {
	// 官方没有维护 next/alpha 标签时必须回落到「全量最高版本」，不能报「无更新」
	info := &npmPackageInfo{
		DistTags: map[string]string{"latest": "0.2.1-rc.5"},
		Time: map[string]string{
			"0.2.1-rc.5": "2026-01-01T00:00:00Z",
			"0.2.2-rc.1": "2026-01-03T00:00:00Z",
		},
	}
	for _, ch := range []string{"next", "alpha"} {
		if got := resolveTargetVersion(info, ch); got != "0.2.2-rc.1" {
			t.Fatalf("resolveTargetVersion(%q) = %q, want fallback 0.2.2-rc.1", ch, got)
		}
	}
}
