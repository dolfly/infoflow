package dynamiccard

import (
	"net/http"
	"testing"
)

func TestResolveBaseURLPrecedence(t *testing.T) {
	const envValue = "https://env.example/open-plat/api/v1"
	getenv := func(name string) string {
		if name == BaseURLEnvVar {
			return envValue
		}
		return ""
	}

	cases := []struct {
		name     string
		explicit string
		getenv   func(string) string
		want     string
	}{
		{"explicit wins", "https://explicit.example/v1", getenv, "https://explicit.example/v1"},
		{"explicit trailing slash", "https://explicit.example/v1/", getenv, "https://explicit.example/v1"},
		{"environment fallback", "", getenv, envValue},
		{"production default", "", func(string) string { return "" }, DefaultBaseURL},
		{"blank explicit is not a value", "   ", getenv, envValue},
		{"nil getenv", "", nil, DefaultBaseURL},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := resolveBaseURL(testCase.explicit, testCase.getenv); got != testCase.want {
				t.Fatalf("resolved base = %q, want %q", got, testCase.want)
			}
		})
	}
}

// T-20: the environment variable overrides the production default.
func TestResolveBaseURLWithEnvironment(t *testing.T) {
	t.Setenv(BaseURLEnvVar, "https://env.example/open-plat/api/v1")
	if got := ResolveBaseURL(""); got != "https://env.example/open-plat/api/v1" {
		t.Fatalf("base = %q, want the environment value", got)
	}
	if got := ResolveBaseURL("https://explicit.example/v1"); got != "https://explicit.example/v1" {
		t.Fatalf("base = %q, want the explicit value", got)
	}
}

func TestDefaultBaseURLIsProduction(t *testing.T) {
	if DefaultBaseURL != "https://open-ruliu.baidu-int.com/open-plat/api/v1" {
		t.Fatalf("default base = %q", DefaultBaseURL)
	}
	if GrayBaseURL != "https://open-ruliu-gray.baidu-int.com/open-plat/api/gray/v1" {
		t.Fatalf("gray base = %q", GrayBaseURL)
	}
	if PreonlineBaseURL != "http://xplatform-preonline.dev.weiyun.baidu.com/open-plat/api/devp/v1" {
		t.Fatalf("preonline base = %q", PreonlineBaseURL)
	}
}

func TestNewHeaders(t *testing.T) {
	headers := NewHeaders("token-1", "log-id-1")
	if got := headers.Get(HeaderContentType); got != ContentTypeJSON {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := headers.Get(HeaderAccessChannel); got != "go_sdk" {
		t.Fatalf("X-OP-ACCESS-CHANNEL = %q", got)
	}
	if got := headers.Get(HeaderLogID); got != "log-id-1" {
		t.Fatalf("LOGID = %q", got)
	}
	if got := headers.Get(HeaderAuthorization); got != "Bearer-token-1" {
		t.Fatalf("Authorization = %q", got)
	}

	anonymous := NewHeaders("", "")
	if anonymous.Get(HeaderAuthorization) != "" {
		t.Fatal("anonymous headers carried Authorization")
	}
	if anonymous.Get(HeaderLogID) != "" {
		t.Fatal("empty LOGID was set")
	}
}

func TestNewLogIDIsUUIDv4(t *testing.T) {
	first, err := NewLogID()
	if err != nil {
		t.Fatalf("new log id: %v", err)
	}
	if !isUUIDv4(first) {
		t.Fatalf("log id = %q, want a version 4 UUID", first)
	}
	second, err := NewLogID()
	if err != nil {
		t.Fatalf("new log id: %v", err)
	}
	if first == second {
		t.Fatal("two LOGIDs are identical")
	}
	if len(first) != 36 {
		t.Fatalf("log id length = %d, want 36", len(first))
	}
	var _ http.Header = NewHeaders("", "")
}
