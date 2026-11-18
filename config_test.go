package infoflow

import "testing"

func TestNormalizeConfigAccessChannelDefault(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "blank", value: "", want: "go_sdk"},
		{name: "whitespace", value: " \t ", want: "go_sdk"},
		{name: "explicit prefix", value: "myapp", want: "myapp_go_sdk"},
		{name: "explicit prefix trimmed", value: "  myapp  ", want: "myapp_go_sdk"},
		{name: "already suffixed idempotent", value: "myapp_go_sdk", want: "myapp_go_sdk"},
		{name: "default value idempotent", value: "go_sdk", want: "go_sdk"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{AppKey: fixtureAppKey, AppSecret: fixtureSecret, AccessChannel: tc.value}
			got, err := normalizeConfig(cfg)
			if err != nil {
				t.Fatalf("normalizeConfig() error = %v", err)
			}
			if got.accessChannel != tc.want {
				t.Errorf("accessChannel = %q, want %q", got.accessChannel, tc.want)
			}
		})
	}
}
