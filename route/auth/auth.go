package auth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type AuthorizedPrincipal struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type storedPublicKey struct {
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"`
}

type keyRing struct {
	Current  *storedPublicKey `json:"current,omitempty"`
	Previous *storedPublicKey `json:"previous,omitempty"`
}

type KeyManager struct {
	publicKeys          map[string]ed25519.PublicKey
	currentKey          *storedPublicKey
	previousKey         *storedPublicKey
	authorizedPrincipal *AuthorizedPrincipal
	mu                  sync.RWMutex
	keyRingPath         string
	legacyKeyPath       string
	principalPath       string
}

var globalKeyManager *KeyManager
var kmOnce sync.Once

func GetKeyManager() *KeyManager {
	kmOnce.Do(func() {
		globalKeyManager = &KeyManager{}
	})
	return globalKeyManager
}

func InitKeyManager(keyDir string) error {
	km := GetKeyManager()
	km.keyRingPath = filepath.Join(keyDir, "public_keys.json")
	km.legacyKeyPath = filepath.Join(keyDir, "public_key.pem")
	km.principalPath = filepath.Join(keyDir, "authorized_principal.json")

	if err := os.MkdirAll(keyDir, 0o755); err != nil {
		return fmt.Errorf("创建密钥目录失败： %w", err)
	}

	var errs []string
	if err := km.loadPublicKeys(); err != nil {
		errs = append(errs, fmt.Sprintf("加载公钥失败： %v", err))
	}

	if err := km.loadAuthorizedPrincipal(); err != nil {
		errs = append(errs, fmt.Sprintf("加载授权主体失败： %v", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "；"))
	}

	return nil
}

func validateKeyID(keyID string) (string, error) {
	normalized := strings.TrimSpace(keyID)
	if normalized == "" {
		return "", fmt.Errorf("密钥 ID 不能为空")
	}
	if len(normalized) > 128 {
		return "", fmt.Errorf("密钥 ID 过长")
	}

	for _, ch := range normalized {
		switch {
		case ch >= 'a' && ch <= 'z':
		case ch >= 'A' && ch <= 'Z':
		case ch >= '0' && ch <= '9':
		case ch == '-', ch == '_', ch == '.':
		default:
			return "", fmt.Errorf("密钥 ID 格式无效")
		}
	}

	return normalized, nil
}

func computeKeyID(pubKeyBase64 string) (string, error) {
	keyBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pubKeyBase64))
	if err != nil {
		return "", fmt.Errorf("公钥 base64 解码失败： %w", err)
	}

	sum := sha256.Sum256(keyBytes)
	return hex.EncodeToString(sum[:]), nil
}

func parsePublicKey(pubKeyBase64 string) (string, ed25519.PublicKey, error) {
	normalized := strings.TrimSpace(pubKeyBase64)
	if normalized == "" {
		return "", nil, fmt.Errorf("公钥不能为空")
	}

	pubKeyBytes, err := base64.StdEncoding.DecodeString(normalized)
	if err != nil {
		return "", nil, fmt.Errorf("公钥 base64 解码失败： %w", err)
	}

	pub, err := x509.ParsePKIXPublicKey(pubKeyBytes)
	if err != nil {
		return "", nil, fmt.Errorf("解析公钥失败： %w", err)
	}

	edPub, ok := pub.(ed25519.PublicKey)
	if !ok {
		return "", nil, fmt.Errorf("公钥不是 Ed25519 类型")
	}

	return normalized, edPub, nil
}

func cloneStoredPublicKey(source *storedPublicKey) *storedPublicKey {
	if source == nil {
		return nil
	}
	return new(*source)
}

func sameStoredPublicKey(left *storedPublicKey, right *storedPublicKey) bool {
	if left == nil || right == nil {
		return left == right
	}

	return left.KeyID == right.KeyID && left.PublicKey == right.PublicKey
}

func normalizeStoredPublicKey(stored *storedPublicKey) (*storedPublicKey, error) {
	if stored == nil {
		return nil, nil
	}

	normalizedPublicKey, _, err := parsePublicKey(stored.PublicKey)
	if err != nil {
		return nil, err
	}

	computedKeyID, err := computeKeyID(normalizedPublicKey)
	if err != nil {
		return nil, err
	}

	keyID := strings.TrimSpace(stored.KeyID)
	if keyID == "" {
		keyID = computedKeyID
	} else {
		keyID, err = validateKeyID(keyID)
		if err != nil {
			return nil, err
		}
		if keyID != computedKeyID {
			return nil, fmt.Errorf("密钥 ID 与公钥不匹配")
		}
	}

	return &storedPublicKey{
		KeyID:     keyID,
		PublicKey: normalizedPublicKey,
	}, nil
}

func (km *KeyManager) refreshPublicKeysLocked() error {
	keys := make(map[string]ed25519.PublicKey)

	if km.currentKey != nil {
		_, currentKey, err := parsePublicKey(km.currentKey.PublicKey)
		if err != nil {
			return err
		}
		keys[km.currentKey.KeyID] = currentKey
	}

	if km.previousKey != nil {
		_, previousKey, err := parsePublicKey(km.previousKey.PublicKey)
		if err != nil {
			return err
		}
		keys[km.previousKey.KeyID] = previousKey
	}

	km.publicKeys = keys
	return nil
}

func (km *KeyManager) savePublicKeysLocked() error {
	data, err := json.MarshalIndent(keyRing{
		Current:  cloneStoredPublicKey(km.currentKey),
		Previous: cloneStoredPublicKey(km.previousKey),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化公钥失败： %w", err)
	}

	if err := os.WriteFile(km.keyRingPath, data, 0o600); err != nil {
		return fmt.Errorf("保存公钥失败： %w", err)
	}

	return nil
}

func (km *KeyManager) loadLegacyPublicKeyLocked() (*storedPublicKey, error) {
	pubKeyPEM, err := os.ReadFile(km.legacyKeyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("公钥文件不存在（未初始化）")
		}
		return nil, fmt.Errorf("读取公钥文件失败： %w", err)
	}

	block, _ := pem.Decode(pubKeyPEM)
	if block == nil {
		return nil, fmt.Errorf("无效的 PEM 格式")
	}

	if _, err := x509.ParsePKIXPublicKey(block.Bytes); err != nil {
		return nil, fmt.Errorf("解析公钥失败： %w", err)
	}

	publicKey := base64.StdEncoding.EncodeToString(block.Bytes)
	keyID, err := computeKeyID(publicKey)
	if err != nil {
		return nil, err
	}

	return &storedPublicKey{
		KeyID:     keyID,
		PublicKey: publicKey,
	}, nil
}

func (km *KeyManager) loadPublicKeys() error {
	km.mu.Lock()
	defer km.mu.Unlock()

	data, err := os.ReadFile(km.keyRingPath)
	switch {
	case err == nil:
		var ring keyRing
		if err := json.Unmarshal(data, &ring); err != nil {
			return fmt.Errorf("解析公钥失败： %w", err)
		}

		currentKey, err := normalizeStoredPublicKey(ring.Current)
		if err != nil {
			return err
		}
		if currentKey == nil {
			return fmt.Errorf("当前公钥不存在（未初始化）")
		}

		previousKey, err := normalizeStoredPublicKey(ring.Previous)
		if err != nil {
			return err
		}

		km.currentKey = currentKey
		if sameStoredPublicKey(currentKey, previousKey) {
			km.previousKey = nil
		} else {
			km.previousKey = previousKey
		}

		return km.refreshPublicKeysLocked()
	case !os.IsNotExist(err):
		return fmt.Errorf("读取公钥文件失败： %w", err)
	}

	legacyKey, err := km.loadLegacyPublicKeyLocked()
	if err != nil {
		return err
	}

	km.currentKey = legacyKey
	km.previousKey = nil
	return km.refreshPublicKeysLocked()
}

func (km *KeyManager) SetPublicKey(pubKeyBase64 string) (bool, error) {
	normalizedPublicKey, _, err := parsePublicKey(pubKeyBase64)
	if err != nil {
		return false, err
	}

	keyID, err := computeKeyID(normalizedPublicKey)
	if err != nil {
		return false, err
	}

	nextCurrentKey := &storedPublicKey{
		KeyID:     keyID,
		PublicKey: normalizedPublicKey,
	}

	km.mu.Lock()
	defer km.mu.Unlock()

	if sameStoredPublicKey(km.currentKey, nextCurrentKey) {
		return false, nil
	}

	previousCurrentKey := cloneStoredPublicKey(km.currentKey)
	previousPreviousKey := cloneStoredPublicKey(km.previousKey)

	km.currentKey = nextCurrentKey
	if previousCurrentKey != nil && !sameStoredPublicKey(previousCurrentKey, nextCurrentKey) {
		km.previousKey = previousCurrentKey
	} else {
		km.previousKey = nil
	}

	if err := km.refreshPublicKeysLocked(); err != nil {
		km.currentKey = previousCurrentKey
		km.previousKey = previousPreviousKey
		_ = km.refreshPublicKeysLocked()
		return false, err
	}

	if err := km.savePublicKeysLocked(); err != nil {
		km.currentKey = previousCurrentKey
		km.previousKey = previousPreviousKey
		_ = km.refreshPublicKeysLocked()
		return false, err
	}

	return true, nil
}

func validateAuthorizedPrincipal(principal *AuthorizedPrincipal) error {
	if principal == nil {
		return fmt.Errorf("授权主体为空")
	}

	principal.Type = strings.TrimSpace(principal.Type)
	principal.Value = strings.TrimSpace(principal.Value)

	switch principal.Type {
	case "uid":
		if principal.Value == "" {
			return fmt.Errorf("UID 不能为空")
		}
		if _, err := strconv.ParseUint(principal.Value, 10, 32); err != nil {
			return fmt.Errorf("UID 格式无效： %w", err)
		}
	case "sid":
		if principal.Value == "" {
			return fmt.Errorf("SID 不能为空")
		}
		if !strings.HasPrefix(principal.Value, "S-") {
			return fmt.Errorf("SID 格式无效")
		}
	default:
		return fmt.Errorf("不支持的授权主体类型: %s", principal.Type)
	}

	return nil
}

func sameAuthorizedPrincipal(left *AuthorizedPrincipal, right *AuthorizedPrincipal) bool {
	if left == nil || right == nil {
		return left == right
	}

	return left.Type == right.Type && left.Value == right.Value
}

func (km *KeyManager) setAuthorizedPrincipal(principal AuthorizedPrincipal) (bool, error) {
	if err := validateAuthorizedPrincipal(&principal); err != nil {
		return false, err
	}

	km.mu.Lock()
	defer km.mu.Unlock()

	if sameAuthorizedPrincipal(km.authorizedPrincipal, &principal) {
		return false, nil
	}

	data, err := json.MarshalIndent(principal, "", "  ")
	if err != nil {
		return false, fmt.Errorf("序列化授权主体失败： %w", err)
	}

	if err := os.WriteFile(km.principalPath, data, 0o600); err != nil {
		return false, fmt.Errorf("保存授权主体失败： %w", err)
	}

	km.authorizedPrincipal = new(principal)
	return true, nil
}

func (km *KeyManager) SetAuthorizedUID(uid uint32) (bool, error) {
	return km.setAuthorizedPrincipal(AuthorizedPrincipal{
		Type:  "uid",
		Value: strconv.FormatUint(uint64(uid), 10),
	})
}

func (km *KeyManager) SetAuthorizedSID(sid string) (bool, error) {
	return km.setAuthorizedPrincipal(AuthorizedPrincipal{
		Type:  "sid",
		Value: strings.TrimSpace(sid),
	})
}

func (km *KeyManager) loadAuthorizedPrincipal() error {
	data, err := os.ReadFile(km.principalPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("授权主体文件不存在（未绑定请求方身份）")
		}
		return fmt.Errorf("读取授权主体文件失败： %w", err)
	}

	var principal AuthorizedPrincipal
	if err := json.Unmarshal(data, &principal); err != nil {
		return fmt.Errorf("解析授权主体失败： %w", err)
	}

	if err := validateAuthorizedPrincipal(&principal); err != nil {
		return err
	}

	km.authorizedPrincipal = new(principal)
	return nil
}

func (km *KeyManager) VerifySignature(keyID string, message string, signature string) error {
	normalizedKeyID, err := validateKeyID(keyID)
	if err != nil {
		return err
	}

	km.mu.RLock()
	publicKey := km.publicKeys[normalizedKeyID]
	km.mu.RUnlock()

	if publicKey == nil {
		return fmt.Errorf("密钥 ID 未注册")
	}

	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return fmt.Errorf("签名解码失败： %w", err)
	}

	if !ed25519.Verify(publicKey, []byte(message), sig) {
		return fmt.Errorf("签名验证失败")
	}

	return nil
}

func (km *KeyManager) IsInitialized() bool {
	km.mu.RLock()
	defer km.mu.RUnlock()
	return km.currentKey != nil && km.currentKey.KeyID != ""
}

func (km *KeyManager) HasAuthorizedPrincipal() bool {
	km.mu.RLock()
	defer km.mu.RUnlock()
	return km.authorizedPrincipal != nil
}

func (km *KeyManager) GetAuthorizedSID() (string, bool) {
	km.mu.RLock()
	defer km.mu.RUnlock()

	if km.authorizedPrincipal == nil || km.authorizedPrincipal.Type != "sid" {
		return "", false
	}

	return km.authorizedPrincipal.Value, true
}

func (km *KeyManager) GetAuthorizedUID() (uint32, bool) {
	km.mu.RLock()
	defer km.mu.RUnlock()

	if km.authorizedPrincipal == nil || km.authorizedPrincipal.Type != "uid" {
		return 0, false
	}

	uid, err := strconv.ParseUint(km.authorizedPrincipal.Value, 10, 32)
	if err != nil {
		return 0, false
	}

	return uint32(uid), true
}

func (km *KeyManager) VerifyRequestPrincipal(r *http.Request) error {
	km.mu.RLock()
	principal := km.authorizedPrincipal
	km.mu.RUnlock()

	if principal == nil {
		return nil
	}

	requestType, requestValue, ok, err := getRequestPrincipal(r)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("当前请求未携带可识别的本地身份信息")
	}
	if requestType != principal.Type {
		return fmt.Errorf("请求方身份类型不匹配")
	}
	if requestValue != principal.Value {
		return fmt.Errorf("请求方身份不匹配")
	}

	return nil
}
