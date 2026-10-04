package logging

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
)

const (
	testRsaPrivateKeyPEM   = "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA0Y1...\n-----END RSA PRIVATE KEY-----"
	testEcPrivateKeyPEM    = "-----BEGIN EC PRIVATE KEY-----\nMHcCAQEEI...\n-----END EC PRIVATE KEY-----"
	testPkcs8PrivateKeyPEM = "-----BEGIN PRIVATE KEY-----\nMIIEvgIBADANBgkqhkiG9w0BAQEFAASCBKgwggSkAgEAAoIBAQD...\n-----END PRIVATE KEY-----"
	testCertPEM            = "-----BEGIN CERTIFICATE-----\nMIIDdzCCAl+gAwIBAgIEAgAAuTANBgkqhkiG9w0BAQsFADBaMQswCQYDVQQGEwJV\n-----END CERTIFICATE-----"
	testPublicKeyPEM       = "-----BEGIN PUBLIC KEY-----\nMIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA0Y1...\n-----END PUBLIC KEY-----"
)

func TestSecretMaterial_PEMDetection(t *testing.T) {
	// Private key variants must be detected
	for _, pem := range []string{testRsaPrivateKeyPEM, testEcPrivateKeyPEM, testPkcs8PrivateKeyPEM} {
		if !ContainsPrivateKeyMaterial(pem) {
			t.Errorf("expected private key PEM to be detected: %s", pem)
		}
		redacted := RedactPrivateKeyMaterial(pem).(string)
		if strings.Contains(redacted, "MII") {
			t.Errorf("expected private key payload to be redacted, got: %s", redacted)
		}
		if !strings.Contains(redacted, PrivateKeyRedactionPlaceholder) {
			t.Errorf("expected placeholder %s in %s", PrivateKeyRedactionPlaceholder, redacted)
		}
	}

	// Public keys and certificates must NOT be flagged as private key material
	for _, pem := range []string{testCertPEM, testPublicKeyPEM} {
		if ContainsPrivateKeyMaterial(pem) {
			t.Errorf("certificate or public key must not be flagged as private key: %s", pem)
		}
		redacted := RedactPrivateKeyMaterial(pem).(string)
		if redacted != pem {
			t.Errorf("public cert/key must remain untouched, got: %s", redacted)
		}
	}
}

func TestSecretMaterial_Base64AndHexDetection(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte(testRsaPrivateKeyPEM))
	if !ContainsPrivateKeyMaterial(b64) {
		t.Errorf("expected base64-encoded private key to be detected")
	}
	redactedB64 := RedactPrivateKeyMaterial(b64).(string)
	if redactedB64 != PrivateKeyRedactionPlaceholder {
		t.Errorf("expected %s, got %s", PrivateKeyRedactionPlaceholder, redactedB64)
	}

	b64Url := base64.RawURLEncoding.EncodeToString([]byte(testRsaPrivateKeyPEM))
	if !ContainsPrivateKeyMaterial(b64Url) {
		t.Errorf("expected base64url-encoded private key to be detected")
	}
	redactedB64Url := RedactPrivateKeyMaterial(b64Url).(string)
	if redactedB64Url != PrivateKeyRedactionPlaceholder {
		t.Errorf("expected %s, got %s", PrivateKeyRedactionPlaceholder, redactedB64Url)
	}

	hexEncoded := hex.EncodeToString([]byte(testRsaPrivateKeyPEM))
	if !ContainsPrivateKeyMaterial(hexEncoded) {
		t.Errorf("expected hex-encoded private key to be detected")
	}
	redactedHex := RedactPrivateKeyMaterial(hexEncoded).(string)
	if redactedHex != PrivateKeyRedactionPlaceholder {
		t.Errorf("expected %s, got %s", PrivateKeyRedactionPlaceholder, redactedHex)
	}
}

func TestSecretMaterial_DERStructures(t *testing.T) {
	// PKCS#12 bundle: SEQUENCE (0x30), length 3, INTEGER (0x02), length 1, value 3
	pfxBundle := []byte{0x30, 0x03, 0x02, 0x01, 0x03, 0x00, 0x00}
	if !LooksPKCS12Bundle(pfxBundle) {
		t.Errorf("expected PKCS#12 bundle to be detected")
	}
	if !ContainsPrivateKeyMaterial(pfxBundle) {
		t.Errorf("expected PKCS#12 buffer to contain private key material")
	}

	// DER private key: SEQUENCE (0x30), length 4, INTEGER (0x02), length 1, version 0, nextTag SEQUENCE (0x30)
	derKey := []byte{0x30, 0x04, 0x02, 0x01, 0x00, 0x30, 0x00, 0x00}
	if !LooksPrivateKeyDER(derKey) {
		t.Errorf("expected DER private key to be detected")
	}
	if !ContainsPrivateKeyMaterial(derKey) {
		t.Errorf("expected DER private key buffer to contain private key material")
	}

	// JKS Keystore: magic 0xfeedfeed, version 1, entry count 2
	jks := make([]byte, 12)
	binary.BigEndian.PutUint32(jks[0:4], JksMagic)
	binary.BigEndian.PutUint32(jks[4:8], 1)
	binary.BigEndian.PutUint32(jks[8:12], 2)
	if !LooksJKSKeystore(jks) {
		t.Errorf("expected JKS keystore to be detected")
	}
	if !ContainsPrivateKeyMaterial(jks) {
		t.Errorf("expected JKS buffer to contain private key material")
	}
}

func TestSecretMaterial_CompressedBuffers(t *testing.T) {
	// Gzip containing private key
	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	_, _ = gw.Write([]byte(testRsaPrivateKeyPEM))
	_ = gw.Close()

	if !ContainsPrivateKeyMaterial(gzBuf.Bytes()) {
		t.Errorf("expected gzip-compressed private key to be detected")
	}

	// Zlib containing private key
	var zlBuf bytes.Buffer
	zw := zlib.NewWriter(&zlBuf)
	_, _ = zw.Write([]byte(testRsaPrivateKeyPEM))
	_ = zw.Close()

	if !ContainsPrivateKeyMaterial(zlBuf.Bytes()) {
		t.Errorf("expected zlib-compressed private key to be detected")
	}

	// Nested compression (gzip inside gzip) must be rejected / fail closed
	var innerBuf bytes.Buffer
	innerGw := gzip.NewWriter(&innerBuf)
	_, _ = innerGw.Write([]byte("innocuous-text"))
	_ = innerGw.Close()

	var outerBuf bytes.Buffer
	outerGw := gzip.NewWriter(&outerBuf)
	_, _ = outerGw.Write(innerBuf.Bytes())
	_ = outerGw.Close()

	if !ContainsPrivateKeyMaterial(outerBuf.Bytes()) {
		t.Errorf("nested compression must fail closed and be rejected as key material")
	}
}

func TestSecretMaterial_GenericSecretsRedaction(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Authorization Bearer header",
			input:    "Authorization: Bearer secret-token-12345",
			expected: "Authorization: [REDACTED]",
		},
		{
			name:     "Authorization Basic header",
			input:    "Authorization=Basic dXNlcjpwYXNz",
			expected: "Authorization=[REDACTED]",
		},
		{
			name:     "Cookie header",
			input:    "Cookie: session_id=xyz; auth_token=abc",
			expected: "Cookie: [REDACTED]",
		},
		{
			name:     "Set-Cookie header",
			input:    "Set-Cookie: user_session=valid; Secure; HttpOnly",
			expected: "Set-Cookie: [REDACTED]",
		},
		{
			name:     "X-API-Key header",
			input:    "X-API-Key: key-abcdef-123456",
			expected: "X-API-Key: [REDACTED]",
		},
		{
			name:     "password assignment with quotes",
			input:    `password="my-super-secret-password"`,
			expected: `password=[REDACTED]`,
		},
		{
			name:     "password assignment without quotes",
			input:    "passwd: pass12345",
			expected: "passwd: [REDACTED]",
		},
		{
			name:     "client_secret assignment",
			input:    "client_secret = oauth-secret-999",
			expected: "client_secret = [REDACTED]",
		},
		{
			name:     "AWS secret access key",
			input:    "aws_secret_access_key: wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			expected: "aws_secret_access_key: [REDACTED]",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactGenericSecrets(tc.input).(string)
			if got != tc.expected {
				t.Fatalf("RedactGenericSecrets(%q) = %q, expected %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestSecretMaterial_FieldNames(t *testing.T) {
	// Sensitive field names
	sensitiveFields := []string{
		"password",
		"PASSWORD",
		"db_password",
		"databasePassword",
		"token",
		"apiKey",
		"api_key",
		"access_key",
		"authorization",
		"clientSecret",
		"cookie",
	}
	for _, f := range sensitiveFields {
		if !FieldNameLooksGenericSecret(f) {
			t.Errorf("expected field name %q to look like generic secret", f)
		}
	}

	// Non-sensitive field names
	nonSensitive := []string{
		"tokenization",
		"tokenCount",
		"tokenExpiry",
		"tokenType",
		"secretary",
		"cookiePolicyEnabled",
	}
	for _, f := range nonSensitive {
		if FieldNameLooksGenericSecret(f) {
			t.Errorf("field name %q should NOT look like generic secret", f)
		}
	}
}
