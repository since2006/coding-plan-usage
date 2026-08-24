package volc

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

const algorithm = "HMAC-SHA256"

type signatureDetails struct {
	Authorization    string
	XDate            string
	XContentSHA256   string
	SignedHeaders    string
	CanonicalRequest string
	StringToSign     string
}

func signRequest(request *http.Request, body []byte, credentials Credentials, now time.Time) signatureDetails {
	xDate := now.UTC().Format("20060102T150405Z")
	shortDate := xDate[:8]
	bodyHash := sha256Hex(body)

	contentType := request.Header.Get("Content-Type")
	if contentType == "" {
		contentType = defaultContentType
		request.Header.Set("Content-Type", contentType)
	}
	request.Header.Set("X-Date", xDate)
	request.Header.Set("X-Content-Sha256", bodyHash)

	headers := map[string]string{
		"content-type":     contentType,
		"host":             canonicalHost(request),
		"x-content-sha256": bodyHash,
		"x-date":           xDate,
	}
	headerNames := make([]string, 0, len(headers))
	for name := range headers {
		headerNames = append(headerNames, name)
	}
	sort.Strings(headerNames)

	var canonicalHeaders strings.Builder
	for _, name := range headerNames {
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(strings.TrimSpace(headers[name]))
		canonicalHeaders.WriteByte('\n')
	}
	signedHeaders := strings.Join(headerNames, ";")

	path := request.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	canonicalQuery := request.URL.Query().Encode()
	canonicalRequest := strings.Join([]string{
		request.Method,
		path,
		canonicalQuery,
		canonicalHeaders.String(),
		signedHeaders,
		bodyHash,
	}, "\n")

	credentialScope := fmt.Sprintf("%s/%s/%s/request", shortDate, credentials.Region, credentials.Service)
	stringToSign := strings.Join([]string{
		algorithm,
		xDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	kDate := hmacSHA256([]byte(credentials.SecretAccessKey), shortDate)
	kRegion := hmacSHA256(kDate, credentials.Region)
	kService := hmacSHA256(kRegion, credentials.Service)
	kSigning := hmacSHA256(kService, "request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))
	authorization := fmt.Sprintf(
		"%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm,
		credentials.AccessKeyID,
		credentialScope,
		signedHeaders,
		signature,
	)
	request.Header.Set("Authorization", authorization)

	return signatureDetails{
		Authorization:    authorization,
		XDate:            xDate,
		XContentSHA256:   bodyHash,
		SignedHeaders:    signedHeaders,
		CanonicalRequest: canonicalRequest,
		StringToSign:     stringToSign,
	}
}

func canonicalHost(request *http.Request) string {
	host := request.Host
	if host == "" {
		host = request.URL.Host
	}
	if strings.HasSuffix(host, ":80") && request.URL.Scheme == "http" {
		return strings.TrimSuffix(host, ":80")
	}
	if strings.HasSuffix(host, ":443") && request.URL.Scheme == "https" {
		return strings.TrimSuffix(host, ":443")
	}
	return host
}

func sha256Hex(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(data))
	return mac.Sum(nil)
}
