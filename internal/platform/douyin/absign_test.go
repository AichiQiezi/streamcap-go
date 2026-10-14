package douyin

import (
	"encoding/hex"
	"strings"
	"testing"
)

// Fixtures generated with the Python reference (streamget.platforms.douyin.ab_sign, streamget as
// installed in StreamCap's venv) with ab_sign's time.time patched to return a fixed value:
//
//	m.time = FixedTime(t); m.ab_sign(q, ua)    # ms below is int(t * 1000)
func TestABSignMatchesPython(t *testing.T) {
	cases := []struct {
		q, ua string
		ms    int64
		want  string
	}{
		{
			q:    "aid=6383&app_name=douyin_web&live_id=1&device_platform=web&language=zh-CN&browser_language=zh-CN&browser_platform=Win32&browser_name=Chrome&browser_version=116.0.0.0&web_rid=898334614374&is_need_double_stream=false&msToken=",
			ua:   "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36",
			ms:   1760000000123,
			want: "E7mhBmg6mEVNgf6X56nLfY3q6fp3Y6Gl0HViMD2fxdVW0L39HMTa9exo1HUvewjji4/sIeYjy4hbO3xprQAjM36UHWwEUdQ2mgWkKl5Q5I0j53iruyRDntmF4vj3SFlm5XNAEOk0y75rKb70Woqe-vIlO62-zo0/91y=",
		},
		{
			q:    "verifyFp=verify_lxj5zv70_7szNlAB7_pxNY_48Vh_ALKF_GA1Uf3yteoOY&type_id=0&live_id=1&room_id=7696161576210664219&sec_user_id=MS4wLjABAAAAvzaICSahsT1tZZEEnFB0DgEa7V0SBoD_Cr9UZAY5NmA&version_code=99.99.99&app_id=1128&is_need_double_stream=True",
			ua:   "Mozilla/5.0 (Linux; Android 11; SAMSUNG SM-G973U) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/14.2 Chrome/87.0.4280.141 Mobile Safari/537.36",
			ms:   1791904000999,
			want: "E7mhBmg6mEVNgf6X5-2LfY3q6Ae3YRCC0HViMD2f7VVdH639HMPq9exoYT4vguEjF4/JIeYjy4hbO3xprQAjM36UHWwEUdQ2mgWkKl5Q5I0j53iruyRDntmF4vj3SFlm5XNAEOk0y75rKb70Woqe-vIlO62-zo0/9vS=",
		},
		{
			q: "", ua: "x", ms: 0,
			want: "E7mhBmg6mEVNgf6X5RVLfY3q6fe3Y6ir0HViMD2fvxVztL39HMYD9exow7zvMKWjZsmfIFYjy4hbO3xprQAjM36UHWwEUdQ2mgWkKl5Q5I0j53iruyRDntmF4vj3SFlm5XNAEOk0y75rKb70Woqe-vIlO62-zo0/96u=",
		},
		{
			q: "a=1", ua: "UA", ms: 1234567890500,
			want: "E7mhBmg6mEVNgf6X5lILfY3q6fB3Y4xm0HViMD2fYdfJSg39HMTg9exoGOkv/dujDG8MIeYjy4hbO3xprQAjM36UHWwEUdQ2mgWkKl5Q5I0j53iruyRDntmF4vj3SFlm5XNAEOk0y75rKb70Woqe-vIlO62-zo0/9If=",
		},
		{
			q: "q=" + strings.Repeat("x", 200), ua: "Mozilla/5.0", ms: 1700000000000,
			want: "E7mhBmg6mEVNgf6X56KLfY3q64Z3YIoI0HViMD2fqdfLqL39HMYD9exoIBGvXKWjwG/-IeYjy4hbO3xprQAjM36UHWwEUdQ2mgWkKl5Q5I0j53iruyRDntmF4vj3SFlm5XNAEOk0y75rKb70Woqe-vIlO62-zo0/95W=",
		},
	}
	orig := nowMillis
	defer func() { nowMillis = orig }()
	for i, c := range cases {
		ms := c.ms
		nowMillis = func() int64 { return ms }
		if got := abSign(c.q, c.ua); got != c.want {
			t.Errorf("case %d: abSign mismatch\n got: %s\nwant: %s", i, got, c.want)
		}
	}
}

func TestSM3MatchesPython(t *testing.T) {
	// Python: SM3().sum(x, 'hex'); "abc" is also the standard GB/T 32905 test vector.
	cases := map[string]string{
		"abc":                    "66c7f0f462eeedd9d1f2d46bdc10e4e24167c4875cf2f7a2297da02b8f4ba8e0",
		strings.Repeat("a", 200): "a8da99c801bbd65ec00992ed5cbef851f275e98da9fb2d4f5be184f81473d5dd",
	}
	for in, want := range cases {
		if got := hex.EncodeToString(newSM3().sum([]byte(in))); got != want {
			t.Errorf("sm3(%q...) = %s, want %s", in[:3], got, want)
		}
	}
}
