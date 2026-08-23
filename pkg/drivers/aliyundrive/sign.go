package aliyundrive

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"

	"github.com/dustinxie/ecc"
)

const (
	secpAppID         = "5dde4e1bdf9e4966b387ba58f4b3fdc3"
	defaultDeviceName = "samsung"
	defaultModelName  = "SM-G9810"
)

func (c *client) configureDevice(userID string) error {
	deviceIDBytes := sha256.Sum256([]byte(userID))
	deviceID := hex.EncodeToString(deviceIDBytes[:])
	privateKey, err := privateKeyFromHex(deviceID)
	if err != nil {
		return err
	}
	signature, err := signatureFor(privateKey, deviceID, userID)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.userID = userID
	c.deviceID = deviceID
	c.signature = signature
	c.mu.Unlock()
	return nil
}

func (c *client) createSessionBody() (map[string]any, error) {
	c.mu.RLock()
	userID := c.userID
	deviceID := c.deviceID
	refreshToken := c.refreshToken
	c.mu.RUnlock()
	if userID == "" || deviceID == "" {
		return nil, fmt.Errorf("aliyundrive: device is not configured")
	}
	privateKey, err := privateKeyFromHex(deviceID)
	if err != nil {
		return nil, err
	}
	signature, err := signatureFor(privateKey, deviceID, userID)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.signature = signature
	c.mu.Unlock()
	return map[string]any{
		"deviceName":   defaultDeviceName,
		"modelName":    defaultModelName,
		"nonce":        0,
		"pubKey":       publicKeyHex(&privateKey.PublicKey),
		"refreshToken": refreshToken,
	}, nil
}

func privateKeyFromHex(value string) (*ecdsa.PrivateKey, error) {
	raw, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("aliyundrive: decode device private key: %w", err)
	}
	curve := ecc.P256k1()
	//lint:ignore SA1019 secp256k1 is not a NIST curve, so crypto/ecdh does not support it; ScalarBaseMult is the only way to derive the public key
	x, y := curve.ScalarBaseMult(raw)
	if x == nil || y == nil {
		return nil, fmt.Errorf("aliyundrive: invalid device private key")
	}
	publicKey := ecdsa.PublicKey{Curve: curve}
	//lint:ignore SA1019 secp256k1 is a custom curve; ecc requires the raw coordinates.
	publicKey.X = x
	//lint:ignore SA1019 secp256k1 is a custom curve; ecc requires the raw coordinates.
	publicKey.Y = y
	privateKey := &ecdsa.PrivateKey{PublicKey: publicKey}
	//lint:ignore SA1019 secp256k1 is a custom curve; ecc requires the raw scalar.
	privateKey.D = new(big.Int).SetBytes(raw)
	return privateKey, nil
}

func signatureFor(privateKey *ecdsa.PrivateKey, deviceID, userID string) (string, error) {
	data := fmt.Sprintf("%s:%s:%s:%d", secpAppID, deviceID, userID, 0)
	digest := sha256.Sum256([]byte(data))
	signature, err := ecc.SignBytes(privateKey, digest[:], ecc.RecID|ecc.LowerS)
	if err != nil {
		return "", fmt.Errorf("aliyundrive: sign device session: %w", err)
	}
	return hex.EncodeToString(signature), nil
}

func publicKeyHex(publicKey *ecdsa.PublicKey) string {
	//lint:ignore SA1019 the aliyun device protocol serializes raw coordinates (X||Y).
	x := publicKey.X
	//lint:ignore SA1019 the aliyun device protocol serializes raw coordinates (X||Y).
	y := publicKey.Y
	return hex.EncodeToString(append(pad32(x), pad32(y)...))
}

func pad32(value *big.Int) []byte {
	raw := value.Bytes()
	if len(raw) >= 32 {
		return raw[len(raw)-32:]
	}
	out := make([]byte, 32)
	copy(out[32-len(raw):], raw)
	return out
}
