package wecom

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
)

const weComPaddingBlockSize = 32

type botCryptor struct {
	token string
	key   []byte
}

func newBotCryptor(token, encodingAESKey string) (*botCryptor, error) {
	key, err := base64.RawStdEncoding.DecodeString(encodingAESKey)
	if err != nil || len(key) != 32 {
		return nil, errors.New("企业微信智能机器人 EncodingAESKey 无效")
	}
	if token == "" {
		return nil, errors.New("企业微信智能机器人 Token 不能为空")
	}
	return &botCryptor{token: token, key: key}, nil
}

func (cryptor *botCryptor) verify(signature, timestamp, nonce, encrypted string) bool {
	if signature == "" || timestamp == "" || nonce == "" || encrypted == "" {
		return false
	}
	expected := cryptor.signature(timestamp, nonce, encrypted)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) == 1
}

func (cryptor *botCryptor) signature(timestamp, nonce, encrypted string) string {
	parts := []string{cryptor.token, timestamp, nonce, encrypted}
	sort.Strings(parts)
	digest := sha1.Sum([]byte(parts[0] + parts[1] + parts[2] + parts[3]))
	return hex.EncodeToString(digest[:])
}

func (cryptor *botCryptor) decrypt(encrypted string) ([]byte, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return nil, errors.New("企业微信智能机器人密文不是有效的 Base64")
	}
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, errors.New("企业微信智能机器人密文长度无效")
	}

	block, err := aes.NewCipher(cryptor.key)
	if err != nil {
		return nil, fmt.Errorf("初始化企业微信智能机器人解密器: %w", err)
	}
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, cryptor.key[:aes.BlockSize]).CryptBlocks(plaintext, ciphertext)
	plaintext, err = unpadWeCom(plaintext)
	if err != nil {
		return nil, err
	}
	if len(plaintext) < 20 {
		return nil, errors.New("企业微信智能机器人明文长度无效")
	}

	messageLength := int(binary.BigEndian.Uint32(plaintext[16:20]))
	if messageLength < 0 || messageLength > len(plaintext)-20 {
		return nil, errors.New("企业微信智能机器人消息长度无效")
	}
	if len(plaintext) != 20+messageLength {
		return nil, errors.New("企业微信智能机器人 ReceiveId 无效")
	}
	return append([]byte(nil), plaintext[20:20+messageLength]...), nil
}

func (cryptor *botCryptor) encrypt(message []byte) (string, error) {
	plaintext := make([]byte, 20+len(message))
	if _, err := rand.Read(plaintext[:16]); err != nil {
		return "", fmt.Errorf("生成企业微信智能机器人随机数: %w", err)
	}
	binary.BigEndian.PutUint32(plaintext[16:20], uint32(len(message)))
	copy(plaintext[20:], message)
	plaintext = padWeCom(plaintext)

	block, err := aes.NewCipher(cryptor.key)
	if err != nil {
		return "", fmt.Errorf("初始化企业微信智能机器人加密器: %w", err)
	}
	ciphertext := make([]byte, len(plaintext))
	cipher.NewCBCEncrypter(block, cryptor.key[:aes.BlockSize]).CryptBlocks(ciphertext, plaintext)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func padWeCom(value []byte) []byte {
	padding := weComPaddingBlockSize - len(value)%weComPaddingBlockSize
	return append(value, bytes.Repeat([]byte{byte(padding)}, padding)...)
}

func unpadWeCom(value []byte) ([]byte, error) {
	if len(value) == 0 {
		return nil, errors.New("企业微信智能机器人填充无效")
	}
	padding := int(value[len(value)-1])
	if padding < 1 || padding > weComPaddingBlockSize || padding > len(value) {
		return nil, errors.New("企业微信智能机器人填充无效")
	}
	for _, character := range value[len(value)-padding:] {
		if int(character) != padding {
			return nil, errors.New("企业微信智能机器人填充无效")
		}
	}
	return value[:len(value)-padding], nil
}
