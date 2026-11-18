package config

import "testing"

func TestMaxActiveTokens(t *testing.T) {
	for _, c := range []struct {
		env  string
		want int
	}{{"", 10}, {"0", 0}, {"3", 3}, {"-1", -1}, {"x", -1}} {
		t.Setenv("PL_MAX_ACTIVE_TOKENS", c.env)
		if got := Load().MaxActiveTokens; got != c.want {
			t.Errorf("%q -> %d, want %d", c.env, got, c.want)
		}
	}
}

func TestMCPRatePerMin(t *testing.T) {
	for _, c := range []struct {
		env  string
		want int
	}{{"", 0}, {"0", 0}, {"60", 60}, {" 5 ", 5}, {"-1", -1}, {"abc", -1}, {"1.5", -1}} {
		t.Setenv("PL_MCP_RATE_PER_MIN", c.env)
		if got := Load().MCPRatePerMin; got != c.want {
			t.Errorf("%q -> %d, want %d", c.env, got, c.want)
		}
	}
}

// 첨부 상한(B23): 기본값, 정상값, 0 이하·숫자 아님은 -1(조립 단계가 기동을 거부한다).
func TestAttachLimits(t *testing.T) {
	for _, c := range []struct {
		env  string
		want int64
	}{{"", 10485760}, {"1", 1}, {" 2048 ", 2048}, {"0", -1}, {"-5", -1}, {"x", -1}, {"1.5", -1}} {
		t.Setenv("PL_ATTACH_MAX_FILE_BYTES", c.env)
		if got := Load().AttachMaxFileBytes; got != c.want {
			t.Errorf("파일당 %q -> %d, want %d", c.env, got, c.want)
		}
	}
	for _, c := range []struct {
		env  string
		want int
	}{{"", 20}, {"3", 3}, {"0", -1}, {"-1", -1}, {"abc", -1}} {
		t.Setenv("PL_ATTACH_MAX_FILES", c.env)
		if got := Load().AttachMaxFiles; got != c.want {
			t.Errorf("개수 %q -> %d, want %d", c.env, got, c.want)
		}
	}
	for _, c := range []struct {
		env  string
		want int64
	}{{"", 52428800}, {"99", 99}, {"0", -1}, {"-1", -1}, {"zz", -1}} {
		t.Setenv("PL_ATTACH_MAX_TOTAL_BYTES", c.env)
		if got := Load().AttachMaxTotalBytes; got != c.want {
			t.Errorf("합계 %q -> %d, want %d", c.env, got, c.want)
		}
	}
}

// 내보내기 상한(B28): 기본 10000, 100~100000 만 허용, 범위 밖·숫자 아님은 -1(조립 단계가 기동을 거부한다).
func TestExportMaxRows(t *testing.T) {
	for _, c := range []struct {
		env  string
		want int
	}{{"", 10000}, {"100", 100}, {" 5000 ", 5000}, {"100000", 100000}, {"99", -1}, {"100001", -1}, {"0", -1}, {"-1", -1}, {"abc", -1}} {
		t.Setenv("PL_EXPORT_MAX_ROWS", c.env)
		if got := Load().ExportMaxRows; got != c.want {
			t.Errorf("PL_EXPORT_MAX_ROWS %q -> %d, want %d", c.env, got, c.want)
		}
	}
}
