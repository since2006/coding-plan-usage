package volc

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSignRequestGolden(t *testing.T) {
	request, err := http.NewRequest(
		http.MethodPost,
		"https://open.volcengineapi.com/?Version=2024-01-01&Region=cn-beijing&Action=GetCodingPlanUsage",
		strings.NewReader(""),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", defaultContentType)
	details := signRequest(request, []byte{}, Credentials{
		AccessKeyID:     "test-ak",
		SecretAccessKey: "test-sk",
		Region:          defaultRegion,
		Service:         defaultService,
	}, time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC))

	wantCanonical := "POST\n" +
		"/\n" +
		"Action=GetCodingPlanUsage&Region=cn-beijing&Version=2024-01-01\n" +
		"content-type:application/json; charset=utf-8\n" +
		"host:open.volcengineapi.com\n" +
		"x-content-sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\n" +
		"x-date:20240102T030405Z\n\n" +
		"content-type;host;x-content-sha256;x-date\n" +
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if details.CanonicalRequest != wantCanonical {
		t.Fatalf("CanonicalRequest mismatch\n--- got ---\n%s\n--- want ---\n%s", details.CanonicalRequest, wantCanonical)
	}
	if got, want := details.SignedHeaders, "content-type;host;x-content-sha256;x-date"; got != want {
		t.Fatalf("SignedHeaders = %q, want %q", got, want)
	}
	const wantAuthorization = "HMAC-SHA256 Credential=test-ak/20240102/cn-beijing/ark/request, SignedHeaders=content-type;host;x-content-sha256;x-date, Signature=f4ce9b0598fe50f5a3898fbbd3294781e34fff58506ffbe5f116560901df8a30"
	if details.Authorization != wantAuthorization {
		t.Fatalf("Authorization = %q", details.Authorization)
	}
	if request.Header.Get("Authorization") != details.Authorization {
		t.Fatal("Authorization header was not set")
	}
}
