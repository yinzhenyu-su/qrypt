package p189

import (
	"crypto/aes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

var b64map = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

func rsaEncode(origData []byte, pubKeyStr string, hex bool) string {
	pubKeyData := "-----BEGIN PUBLIC KEY-----\n" + pubKeyStr + "\n-----END PUBLIC KEY-----"
	block, _ := pem.Decode([]byte(pubKeyData))
	pubInterface, _ := x509.ParsePKIXPublicKey(block.Bytes)
	pub := pubInterface.(*rsa.PublicKey)
	b, err := rsa.EncryptPKCS1v15(rand.Reader, pub, origData)
	if err != nil {
		return ""
	}
	res := base64.StdEncoding.EncodeToString(b)
	if hex {
		return b64toHex(res)
	}
	return res
}

func b64toHex(a string) string {
	d := ""
	e := 0
	c := 0
	for i := 0; i < len(a); i++ {
		m := string(a[i])
		if m != "=" {
			v := strings.Index(b64map, m)
			if 0 == e {
				e = 1
				d += fmt.Sprintf("%x", v>>2)
				c = 3 & v
			} else if 1 == e {
				e = 2
				d += fmt.Sprintf("%x", c<<2|v>>4)
				c = 15 & v
			} else if 2 == e {
				e = 3
				d += fmt.Sprintf("%x", c)
				d += fmt.Sprintf("%x", v>>2)
				c = 3 & v
			} else {
				e = 0
				d += fmt.Sprintf("%x", c<<2|v>>4)
				d += fmt.Sprintf("%x", 15&v)
			}
		}
	}
	if e == 1 {
		d += fmt.Sprintf("%x", c<<2)
	}
	return d
}

func aesEncrypt(data, key []byte) []byte {
	block, _ := aes.NewCipher(key)
	if block == nil {
		return nil
	}
	data = pkcs7Padding(data, block.BlockSize())
	decrypted := make([]byte, len(data))
	size := block.BlockSize()
	for bs, be := 0, size; bs < len(data); bs, be = bs+size, be+size {
		block.Encrypt(decrypted[bs:be], data[bs:be])
	}
	return decrypted
}

func pkcs7Padding(ciphertext []byte, blockSize int) []byte {
	padding := blockSize - len(ciphertext)%blockSize
	padtext := make([]byte, len(ciphertext)+padding)
	copy(padtext, ciphertext)
	for i := len(ciphertext); i < len(padtext); i++ {
		padtext[i] = byte(padding)
	}
	return padtext
}

func hmacSha1(data, secret string) string {
	h := hmac.New(sha1.New, []byte(secret))
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

func randomString(pattern string) string {
	re := regexp.MustCompile("[xy]")
	return re.ReplaceAllStringFunc(pattern, func(match string) string {
		var i int64
		n, _ := rand.Int(rand.Reader, big.NewInt(16))
		t := n.Int64()
		if match == "x" {
			i = t
		} else {
			i = 3&t | 8
		}
		return fmt.Sprintf("%x", i)
	})
}

func formEncode(form map[string]string) string {
	vals := url.Values{}
	for k, v := range form {
		vals.Set(k, v)
	}
	return encodeValues(vals)
}

func encodeValues(v url.Values) string {
	if v == nil {
		return ""
	}
	var buf strings.Builder
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		vs := v[k]
		for _, v := range vs {
			if buf.Len() > 0 {
				buf.WriteByte('&')
			}
			buf.WriteString(k)
			buf.WriteByte('=')
			buf.WriteString(v)
		}
	}
	return buf.String()
}

func generateUploadHeaders(sessionKey, uri string, form map[string]string, pubKey, pkID string) (map[string]string, string, error) {
	date := fmt.Sprintf("%d", time.Now().UnixMilli())
	reqID := randomString("xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx")
	key := randomString("xxxxxxxxxxxx4xxxyxxxxxxxxxxxxxxx")
	keyLen := 16 + int(16*float64(time.Now().UnixNano()%100000)/100000.0)
	if keyLen > len(key) {
		keyLen = len(key)
	}
	if keyLen < 16 {
		keyLen = 16
	}
	key = key[:keyLen]

	encoded := formEncode(form)
	encrypted := aesEncrypt([]byte(encoded), []byte(key[:16]))
	params := hex.EncodeToString(encrypted)

	sigInput := fmt.Sprintf("SessionKey=%s&Operate=GET&RequestURI=%s&Date=%s&params=%s", sessionKey, uri, date, params)
	signature := hmacSha1(sigInput, key)

	encKey := rsaEncode([]byte(key), pubKey, false)

	headers := map[string]string{
		"accept":         "application/json;charset=UTF-8",
		"SessionKey":     sessionKey,
		"Signature":      signature,
		"X-Request-Date": date,
		"X-Request-ID":   reqID,
		"EncryptionText": encKey,
		"PkId":           pkID,
	}
	return headers, params, nil
}
