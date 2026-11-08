package logs

import "testing"

func TestMask(t *testing.T) {
	for in, want := range map[string]string{
		"GET /api/v1/products/42 200 12ms":                       "GET /api/v1/products/<num> <num> <duration>",
		"request_id=3f2a1b9c-6d4e-4f8a-9b7c-2d1e0f3a4b5c done":   "request_id=<uuid> done",
		"dial tcp 10.96.12.34:6379: connect: connection refused": "dial tcp <ip>: connect: connection refused",
		"dial tcp [fd00::1]:6379: i/o timeout":                   "dial tcp <ip>: i/o timeout",
		"took 3m20s, limit 512Mi":                                "took <duration>, limit <size>",
		"at 2026-11-08T21:14:11.123Z retry 3 of 5":               "at <time> retry <num> of <num>",
		"pc=0x4a3b2c addr=0x0 sp=7ffd5e8a9b10":                   "pc=<hex> addr=<hex> sp=<hex>",
		"user (id 1234) not found.":                              "user (id <num>) not found.",
		"facade decade accepted":                                 "facade decade accepted",
		"sha256 http2 utf8 v2 x509":                              "sha256 http2 utf8 v2 x509",
		"password=[REDACTED:secret-value] rejected":              "password=[REDACTED:secret-value] rejected",
		"started at 21:14:11.5 in 1.5s":                          "started at <time> in <duration>",
		"cpu=-0.5 mem=+12":                                       "cpu=<num> mem=<num>",
	} {
		if got := Mask(in); got != want {
			t.Errorf("Mask(%q)\n  = %q\nwant %q", in, got, want)
		}
	}
}
