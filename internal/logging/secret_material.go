package logging

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"regexp"
	"strings"
)

const (
	PrivateKeyRedactionPlaceholder    = "[PRIVATE_KEY_REDACTED]"
	GenericSecretRedactionPlaceholder = "[REDACTED]"
	PrivateKeyMaterialRejected        = "PRIVATE_KEY_MATERIAL_REJECTED"

	MaxScanDepth                = 12
	MaxDerTLVLength             = 1024 * 1024 // 1 MiB
	MaxDerChildren              = 16
	MaxDerOIDLength             = 128
	MaxCompressedInputLength    = 1024 * 1024     // 1 MiB
	MaxDecompressedOutputLength = 4 * 1024 * 1024 // 4 MiB
	DecompressedOutputRatio     = 4
	JksMagic                    = uint32(0xfeedfeed)
)

var (
	ErrPrivateKeyMaterialRejected = errors.New("private key material is not accepted")

	privateKeyPemPattern = regexp.MustCompile(`(?i)-----\s*BEGIN\s+(?:[A-Z0-9]+\s+)*PRIVATE\s+KEY\s*-----`)
	privateKeyBlockRegex = regexp.MustCompile(`(?i)-----\s*BEGIN\s+(?:[A-Z0-9]+\s+)*PRIVATE\s+KEY\s*-----[\s\S]*?-----\s*END\s+(?:[A-Z0-9]+\s+)*PRIVATE\s+KEY\s*-----`)

	authValueRegex  = regexp.MustCompile(`(?i)\b(Authorization\s*[:=]\s*)(?:Bearer|Basic|Token)\s+("[^"]*"|'[^']*'|[^\s,;]+)`)
	cookieRegex     = regexp.MustCompile(`(?i)\b((?:Set-)?Cookie\s*:\s*)([^\r\n]*)`)
	apiTokenRegex   = regexp.MustCompile(`(?i)\b((?:X[\s_-]?(?:API[\s_-]?Key|Auth[\s_-]?Token))\s*:\s*)("[^"]*"|'[^']*'|[^\s,;]+)`)
	assignmentRegex = regexp.MustCompile(`(?i)\b((?:password|passwd|credential|secret|api[\s_-]?key|api[\s_-]?secret|token(?:[\s_-]?(?:secret|value))?|bearer[\s_-]?token|access[\s_-]?token|refresh[\s_-]?token|client[\s_-]?secret|aws[\s_-]?secret[\s_-]?access[\s_-]?key)\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;]+)`)

	privateKeyFieldFragments = []string{
		"privatekey",
		"privatekeypem",
		"encryptedprivatekey",
		"keymaterial",
		"pfxblob",
		"jksblob",
		"tlskey",
		"caprivatekey",
		"rawprivatekey",
		"rawkey",
		"pemprivatekey",
		"privatepem",
		"keypem",
		"pfx",
		"jks",
		"keystore",
	}

	genericSecretFieldNames = []string{
		"token",
		"apitoken",
		"authtoken",
		"authorizationtoken",
		"bearertoken",
		"sessiontoken",
		"secrettoken",
		"accesstoken",
		"refreshtoken",
		"idtoken",
		"xauthtoken",
		"xapikey",
		"password",
		"passwd",
		"passphrase",
		"credential",
		"credentials",
		"secret",
		"secretkey",
		"rawsecret",
		"tokensecret",
		"tokenvalue",
		"apisecret",
		"apikey",
		"bearer",
		"authorization",
		"cookie",
		"setcookie",
		"cookieheader",
		"accesskey",
		"accesskeyid",
		"clientsecret",
		"awssecretaccesskey",
	}

	genericSecretFieldSuffixes = []string{
		"token",
		"apitoken",
		"authtoken",
		"authorizationtoken",
		"bearertoken",
		"sessiontoken",
		"secrettoken",
		"idtoken",
		"xauthtoken",
		"xapikey",
		"setcookie",
		"cookieheader",
		"password",
		"passwd",
		"passphrase",
		"credential",
		"credentials",
		"secret",
		"secretkey",
		"rawsecret",
		"tokensecret",
		"tokenvalue",
		"apikey",
		"apisecret",
		"accesstoken",
		"refreshtoken",
		"clientsecret",
		"awssecretaccesskey",
	}
)

func normalizeFieldName(val string) string {
	lower := strings.ToLower(val)
	var b strings.Builder
	for i := 0; i < len(lower); i++ {
		c := lower[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// FieldNameLooksPrivateKeyMaterial checks if a field name indicates private key content.
func FieldNameLooksPrivateKeyMaterial(fieldName string) bool {
	norm := normalizeFieldName(fieldName)
	for _, frag := range privateKeyFieldFragments {
		if strings.Contains(norm, frag) {
			return true
		}
	}
	return false
}

// FieldNameLooksGenericSecret checks if a field name indicates secret or credential content.
func FieldNameLooksGenericSecret(fieldName string) bool {
	norm := normalizeFieldName(fieldName)
	for _, exact := range genericSecretFieldNames {
		if norm == exact {
			return true
		}
	}
	for _, suffix := range genericSecretFieldSuffixes {
		if strings.HasSuffix(norm, suffix) {
			return true
		}
	}
	return false
}

func looksBase64(value string) bool {
	compact := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(value, " ", ""), "\t", ""), "\n", "")
	compact = strings.ReplaceAll(compact, "\r", "")
	if len(compact) < 16 {
		return false
	}
	// Check alphabet: standard or base64url
	for i := 0; i < len(compact); i++ {
		c := compact[i]
		isStd := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '+' || c == '/' || c == '='
		isUrl := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '='
		if !isStd && !isUrl {
			return false
		}
	}
	return true
}

func base64DecodeIfLikely(value string) []byte {
	if !looksBase64(value) {
		return nil
	}
	compact := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(value, " ", ""), "\t", ""), "\n", "")
	compact = strings.ReplaceAll(compact, "\r", "")

	// Choose decoder based on characters present
	if strings.ContainsAny(compact, "-_") {
		// Base64URL
		if raw, err := base64.RawURLEncoding.DecodeString(compact); err == nil {
			return raw
		}
		if raw, err := base64.URLEncoding.DecodeString(compact); err == nil {
			return raw
		}
	} else {
		// Standard
		if raw, err := base64.RawStdEncoding.DecodeString(compact); err == nil {
			return raw
		}
		if raw, err := base64.StdEncoding.DecodeString(compact); err == nil {
			return raw
		}
	}
	return nil
}

func hexDecodeIfLikely(value string) []byte {
	compact := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(value, " ", ""), "\t", ""), "\n", "")
	compact = strings.ReplaceAll(compact, "\r", "")
	if len(compact) < 16 || len(compact)%2 != 0 {
		return nil
	}
	raw, err := hex.DecodeString(compact)
	if err != nil {
		return nil
	}
	return raw
}

func derHeaderLength(b []byte) int {
	if len(b) < 2 {
		return -1
	}
	lengthByte := b[1]
	if (lengthByte & 0x80) == 0 {
		return 2
	}
	lengthBytes := int(lengthByte & 0x7f)
	if lengthBytes == 0 || lengthBytes > 4 || len(b) < 2+lengthBytes {
		return -1
	}
	return 2 + lengthBytes
}

type derTLV struct {
	tag                    byte
	valueStart             int
	valueEnd               int
	valueLength            int
	exceedsInspectionLimit bool
}

func readDerTLV(data []byte, offset, boundary int) *derTLV {
	if data == nil || offset < 0 || boundary < offset || boundary > len(data) || offset+2 > boundary {
		return nil
	}

	lengthByte := data[offset+1]
	var valueLength int
	headerLength := 2

	if (lengthByte & 0x80) == 0 {
		valueLength = int(lengthByte)
	} else {
		lengthBytes := int(lengthByte & 0x7f)
		if lengthBytes == 0 || lengthBytes > 4 || offset+2+lengthBytes > boundary {
			return nil
		}
		if data[offset+2] == 0 {
			return nil
		}
		for i := 0; i < lengthBytes; i++ {
			valueLength = valueLength*256 + int(data[offset+2+i])
		}
		if valueLength < 128 {
			return nil
		}
		headerLength += lengthBytes
	}

	valueStart := offset + headerLength
	valueEnd := valueStart + valueLength
	if valueEnd > boundary || valueEnd < valueStart {
		return nil
	}

	return &derTLV{
		tag:                    data[offset],
		valueStart:             valueStart,
		valueEnd:               valueEnd,
		valueLength:            valueLength,
		exceedsInspectionLimit: valueLength > MaxDerTLVLength,
	}
}

// LooksPKCS12Bundle detects a PKCS#12/PFX-like DER envelope: SEQUENCE { INTEGER 3 ... }.
func LooksPKCS12Bundle(b []byte) bool {
	if len(b) < 7 || b[0] != 0x30 {
		return false
	}
	headerLen := derHeaderLength(b)
	if headerLen < 0 || len(b) < headerLen+3 {
		return false
	}
	return b[headerLen] == 0x02 && b[headerLen+1] == 0x01 && b[headerLen+2] == 0x03
}

// LooksPrivateKeyDER detects PKCS#8, PKCS#1 RSA, and SEC1 EC private key DER envelopes.
func LooksPrivateKeyDER(b []byte) bool {
	if len(b) < 8 || b[0] != 0x30 {
		return false
	}
	headerLen := derHeaderLength(b)
	if headerLen < 0 || len(b) < headerLen+4 {
		return false
	}
	if b[headerLen] != 0x02 || b[headerLen+1] != 0x01 {
		return false
	}
	version := b[headerLen+2]
	if version != 0x00 && version != 0x01 {
		return false
	}
	nextTag := b[headerLen+3]
	return nextTag == 0x30 || nextTag == 0x02 || (version == 0x01 && nextTag == 0x04)
}

// LooksJKSKeystore detects Java KeyStore (JKS) binary magic header 0xFEEDFEED.
func LooksJKSKeystore(b []byte) bool {
	if len(b) < 12 {
		return false
	}
	magic := binary.BigEndian.Uint32(b[0:4])
	if magic != JksMagic {
		return false
	}
	version := binary.BigEndian.Uint32(b[4:8])
	if version != 1 && version != 2 {
		return false
	}
	entryCount := int32(binary.BigEndian.Uint32(b[8:12]))
	return entryCount >= 0
}

// LooksEncryptedPKCS8DER detects PKCS#8 EncryptedPrivateKeyInfo DER: SEQUENCE { AlgorithmIdentifier, OCTET STRING }.
func LooksEncryptedPKCS8DER(b []byte) bool {
	outer := readDerTLV(b, 0, len(b))
	if outer == nil || outer.tag != 0x30 || outer.valueEnd != len(b) {
		return false
	}
	algo := readDerTLV(b, outer.valueStart, outer.valueEnd)
	if algo == nil || algo.tag != 0x30 {
		return false
	}
	encData := readDerTLV(b, algo.valueEnd, outer.valueEnd)
	if encData == nil || encData.tag != 0x04 || encData.valueLength == 0 || encData.valueEnd != outer.valueEnd {
		return false
	}
	// Check OID in AlgorithmIdentifier
	oid := readDerTLV(b, algo.valueStart, algo.valueEnd)
	if oid == nil || oid.tag != 0x06 {
		return false
	}
	return true
}

func looksGzipHeader(b []byte) bool {
	return len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b
}

func looksZlibHeader(b []byte) bool {
	if len(b) < 2 {
		return false
	}
	cmf := b[0]
	flg := b[1]
	if (cmf & 0x0f) != 8 {
		return false
	}
	if (cmf >> 4) > 7 {
		return false
	}
	return ((uint16(cmf)<<8)|uint16(flg))%31 == 0
}

func zlibHasPresetDict(b []byte) bool {
	return len(b) >= 2 && (b[1]&0x20) != 0
}

type compressionClassification int

const (
	compNotCompressed compressionClassification = iota
	compDecompressed
	compRejected
)

func classifyCompressedBuffer(b []byte) (compressionClassification, []byte) {
	if b == nil {
		return compNotCompressed, nil
	}
	isGzip := looksGzipHeader(b)
	isZlib := !isGzip && looksZlibHeader(b)
	if !isGzip && !isZlib {
		return compNotCompressed, nil
	}

	if len(b) > MaxCompressedInputLength {
		return compRejected, nil
	}
	if isZlib && zlibHasPresetDict(b) {
		return compRejected, nil
	}

	maxOutputLength := len(b) * DecompressedOutputRatio
	if maxOutputLength > MaxDecompressedOutputLength {
		maxOutputLength = MaxDecompressedOutputLength
	}

	var r io.Reader

	if isGzip {
		gz, gzErr := gzip.NewReader(bytes.NewReader(b))
		if gzErr != nil {
			return compRejected, nil
		}
		defer gz.Close()
		r = gz
	} else {
		zl, zlErr := zlib.NewReader(bytes.NewReader(b))
		if zlErr != nil {
			return compRejected, nil
		}
		defer zl.Close()
		r = zl
	}

	var out bytes.Buffer
	limited := io.LimitReader(r, int64(maxOutputLength)+1)
	n, readErr := io.Copy(&out, limited)
	if readErr != nil || n > int64(maxOutputLength) {
		return compRejected, nil
	}

	decoded := out.Bytes()
	// Rejection on nested compression (gzip-of-gzip or zlib-of-gzip)
	if looksGzipHeader(decoded) || looksZlibHeader(decoded) {
		return compRejected, nil
	}

	return compDecompressed, decoded
}

func ordinaryBufferContainsPrivateKey(b []byte) bool {
	if b == nil {
		return false
	}
	if LooksPKCS12Bundle(b) || LooksPrivateKeyDER(b) || LooksEncryptedPKCS8DER(b) || LooksJKSKeystore(b) {
		return true
	}
	return privateKeyPemPattern.Match(b)
}

func bufferContainsPrivateKey(b []byte) bool {
	if b == nil {
		return false
	}
	classification, decompressed := classifyCompressedBuffer(b)
	if classification == compRejected {
		return true
	}
	if classification == compDecompressed {
		return ordinaryBufferContainsPrivateKey(decompressed)
	}
	return ordinaryBufferContainsPrivateKey(b)
}

func stringContainsPrivateKey(s string) bool {
	if len(s) == 0 {
		return false
	}
	if privateKeyPemPattern.MatchString(s) {
		return true
	}
	if hexDecoded := hexDecodeIfLikely(s); hexDecoded != nil {
		if ordinaryBufferContainsPrivateKey(hexDecoded) {
			return true
		}
	}
	if base64Decoded := base64DecodeIfLikely(s); base64Decoded != nil {
		if bufferContainsPrivateKey(base64Decoded) {
			return true
		}
	}
	return false
}

// ContainsPrivateKeyMaterial deep-scans any value for private key material.
func ContainsPrivateKeyMaterial(val any) bool {
	return containsPrivateKeyMaterialInternal(val, 0)
}

func containsPrivateKeyMaterialInternal(val any, depth int) bool {
	if val == nil {
		return false
	}
	if depth > MaxScanDepth {
		return true
	}

	switch v := val.(type) {
	case string:
		return stringContainsPrivateKey(v)
	case []byte:
		return bufferContainsPrivateKey(v)
	case []any:
		for _, item := range v {
			if containsPrivateKeyMaterialInternal(item, depth+1) {
				return true
			}
		}
	case map[string]any:
		for _, item := range v {
			if containsPrivateKeyMaterialInternal(item, depth+1) {
				return true
			}
		}
	}
	return false
}

// AssertNoPrivateKeyMaterial returns ErrPrivateKeyMaterialRejected if private key material is present.
func AssertNoPrivateKeyMaterial(val any) error {
	if ContainsPrivateKeyMaterial(val) {
		return ErrPrivateKeyMaterialRejected
	}
	return nil
}

// RedactPrivateKeyMaterial replaces any private key PEM blocks or encoded keys with [PRIVATE_KEY_REDACTED].
func RedactPrivateKeyMaterial(val any) any {
	s, ok := val.(string)
	if !ok {
		return val
	}
	if privateKeyPemPattern.MatchString(s) {
		return privateKeyBlockRegex.ReplaceAllString(s, PrivateKeyRedactionPlaceholder)
	}
	if base64Decoded := base64DecodeIfLikely(s); base64Decoded != nil {
		if bufferContainsPrivateKey(base64Decoded) {
			return PrivateKeyRedactionPlaceholder
		}
	}
	if hexDecoded := hexDecodeIfLikely(s); hexDecoded != nil {
		if ordinaryBufferContainsPrivateKey(hexDecoded) {
			return PrivateKeyRedactionPlaceholder
		}
	}
	return s
}

func redactGenericString(s string) string {
	res := authValueRegex.ReplaceAllString(s, `${1}`+GenericSecretRedactionPlaceholder)
	res = cookieRegex.ReplaceAllString(res, `${1}`+GenericSecretRedactionPlaceholder)
	res = apiTokenRegex.ReplaceAllString(res, `${1}`+GenericSecretRedactionPlaceholder)
	res = assignmentRegex.ReplaceAllString(res, `${1}`+GenericSecretRedactionPlaceholder)
	return res
}

// RedactGenericSecrets scans and redacts generic secrets (passwords, tokens, cookies, auth headers).
func RedactGenericSecrets(val any) any {
	return redactGenericSecretsInternal(val, 0, make(map[any]struct{}))
}

func redactGenericSecretsInternal(val any, depth int, seen map[any]struct{}) any {
	if val == nil {
		return nil
	}
	if depth > MaxScanDepth {
		return "[REDACTED:max-depth]"
	}

	switch v := val.(type) {
	case string:
		return redactGenericString(v)
	case []byte:
		return v
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = redactGenericSecretsInternal(item, depth+1, seen)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			if FieldNameLooksGenericSecret(k) {
				out[k] = GenericSecretRedactionPlaceholder
			} else {
				out[k] = redactGenericSecretsInternal(item, depth+1, seen)
			}
		}
		return out
	default:
		return val
	}
}

// ContainsGenericSecretMaterial checks if a value contains unredacted generic secret material.
func ContainsGenericSecretMaterial(val any) bool {
	s, ok := val.(string)
	if !ok {
		return false
	}
	for _, re := range []*regexp.Regexp{authValueRegex, cookieRegex, apiTokenRegex, assignmentRegex} {
		matches := re.FindAllStringSubmatch(s, -1)
		for _, m := range matches {
			if len(m) >= 3 {
				target := strings.Trim(strings.TrimSpace(m[2]), `"'`)
				if target != GenericSecretRedactionPlaceholder && !strings.HasPrefix(target, GenericSecretRedactionPlaceholder) {
					return true
				}
			}
		}
	}
	return false
}
