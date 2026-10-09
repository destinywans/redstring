package logredact

import (
	"strings"
	"testing"
)

func TestMaskSecretsAndPersonalData(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		kinds          Counts
	}{
		{"bearer header", `GET /orders Authorization: Bearer abcdefghijklmnop1234 status=200`,
			`GET /orders Authorization: Bearer [REDACTED:token] status=200`, Counts{Token: 1}},
		{"jwt in json", `{"msg":"login","jwt":"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"}`,
			`{"msg":"login","jwt":"[REDACTED:token]"}`, Counts{Token: 1}},
		{"password pair", `db connect user=app password=s3cr3t! host=db`,
			`db connect user=app password=[REDACTED:secret] host=db`, Counts{Secret: 1}},
		{"json secret", `{"client_secret": "x8Y2-zz", "level":"error"}`,
			`{"client_secret": "[REDACTED:secret]", "level":"error"}`, Counts{Secret: 1}},
		{"api key query", `GET /v1/data?api_key=AbC123xyz&page=2`,
			`GET /v1/data?api_key=[REDACTED:secret]&page=2`, Counts{Secret: 1}},
		{"url credential", `dial postgres://orders:hunter2@10.1.2.3:5432/orders failed`,
			`dial postgres://[REDACTED:credential]@[REDACTED:ip]:5432/orders failed`, Counts{Credential: 1, IP: 1}},
		{"cookie header", `request Cookie: sid=abc123; theme=dark`,
			`request Cookie: [REDACTED:cookie]`, Counts{Cookie: 1}},
		{"aws key", `using AKIAIOSFODNN7EXAMPLE for s3`, `using [REDACTED:cloud_key] for s3`, Counts{CloudKey: 1}},
		{"github token", `token ghp_abcdefghijklmnopqrstuvwxyz0123456789 expired`,
			`token [REDACTED:token] expired`, Counts{Token: 1}},
		{"private key", "key=-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA\n-----END RSA PRIVATE KEY----- loaded",
			"key=[REDACTED:private_key] loaded", Counts{PrivateKey: 1}},
		{"email", `notify ops.lead@example.com about INV-231`, `notify [REDACTED:email] about INV-231`, Counts{Email: 1}},
		{"mobile phone", `sms to 13812345678 sent`, `sms to [REDACTED:phone] sent`, Counts{Phone: 1}},
		{"intl phone", `call +8613812345678 now`, `call [REDACTED:phone] now`, Counts{Phone: 1}},
		{"id card", `kyc id=11010519491231002X ok`, `kyc id=[REDACTED:id_card] ok`, Counts{IDCard: 1}},
		{"bank card", `charge card 4111 1111 1111 1111 declined`, `charge card [REDACTED:bank_card] declined`, Counts{BankCard: 1}},
		{"bank card plain", `pan=6222021234567890128`, `pan=[REDACTED:bank_card]`, Counts{BankCard: 1}},
		{"internal ip", `upstream 192.168.10.24 timeout, peer 172.20.0.5`, `upstream [REDACTED:ip] timeout, peer [REDACTED:ip]`, Counts{IP: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, counts := Mask(tc.in)
			if got != tc.want {
				t.Fatalf("Mask(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
			if len(counts) != len(tc.kinds) {
				t.Fatalf("counts %v, want %v", counts, tc.kinds)
			}
			for kind, n := range tc.kinds {
				if counts[kind] != n {
					t.Fatalf("counts %v, want %v", counts, tc.kinds)
				}
			}
		})
	}
}

// Identifiers and timestamps are investigation evidence and must survive.
func TestMaskKeepsIdentifiersAndTimestamps(t *testing.T) {
	for _, line := range []string{
		`{"level":"error","trace_id":"9d2870f771d3eac3a7faeb4db4515e00","span_id":"60e223210a47ab56","msg":"timeout"}`,
		`order_id=a9750af711c67497e2cad0a8d9af3c0f request_id=4ae5228beea970276650054b6a7f3584`,
		`ts=1791537828184 epoch_ns=1791537828184901286 seconds=1791537828`,
		`at 2026-10-09T11:39:08.663697Z duration_ms=16 status=500 route=/orders`,
		`compact=20261009123045123 build=20261009`,
		`uuid=d262c6a3-5c8c-4560-9026-e31fd977b5a2 pod=order-api-78c8684ff4-qc7wc`,
		`token_count=5512 author=alice tokens=1200 secretary=bob`,
		`card declined count=4 after 3 retries`,
		`public peer 8.8.8.8 and 172.32.0.1 and 11.0.0.1`,
		`version 1.27.1 image sha256:3f1c0b2a9d8e`,
		`order 4111111111111112 is not a valid card`,
	} {
		got, counts := Mask(line)
		if got != line || counts.Total() != 0 {
			t.Fatalf("over-masked %q -> %q (%v)", line, got, counts)
		}
	}
}

func TestMaskIsIdempotentAndCountsOnce(t *testing.T) {
	once, first := Mask(`password=abc Authorization: Bearer abcdefghijklmnop1234 mail a@b.io`)
	twice, second := Mask(once)
	if once != twice || second.Total() != 0 || first.Total() != 3 {
		t.Fatalf("not idempotent: %q -> %q (%v, %v)", once, twice, first, second)
	}
	// An already masked token value is not counted again as a secret pair.
	if _, counts := Mask(`token=eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U`); counts[Token] != 1 || counts[Secret] != 0 {
		t.Fatalf("double counted: %v", counts)
	}
	kept, counts := MaskWith(`peer 10.0.0.7`, Options{KeepInternalIPs: true})
	if kept != `peer 10.0.0.7` || counts.Total() != 0 {
		t.Fatalf("KeepInternalIPs: %q %v", kept, counts)
	}
	if !strings.Contains(strings.Join(Counts{Email: 1, Token: 2}.Kinds(), ","), "email,token") {
		t.Fatal("kinds are not sorted")
	}
}
