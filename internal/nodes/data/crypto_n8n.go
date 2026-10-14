package data

import (
	"crypto"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // offered for compatibility, not security
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // offered for compatibility, not security
	"crypto/sha256"
	"crypto/sha3"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"math/big"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/monoes/mono-agent/internal/workflow"
	//lint:ignore SA1019 n8n's Crypto node offers RIPEMD-160, so imported workflows request it
	"golang.org/x/crypto/ripemd160"
)

// n8n Crypto (v2) actions on data.crypto: hash, hmac, sign, generate,
// encrypt and decrypt. The value to process ("value") is usually an
// expression and is evaluated per item; the result goes to
// data_property_name (default "data").
var cryptoActions = map[string]bool{"hash": true, "hmac": true, "sign": true, "generate": true, "encrypt": true, "decrypt": true}

type hashType struct {
	newHash func() hash.Hash
	id      crypto.Hash
}

// hashTypes maps the algorithm names n8n offers (plus SHA224, SHA3-224 and
// RIPEMD160) to constructors.
var hashTypes = map[string]hashType{
	"MD5":       {md5.New, crypto.MD5},
	"SHA1":      {sha1.New, crypto.SHA1},
	"SHA224":    {sha256.New224, crypto.SHA224},
	"SHA256":    {sha256.New, crypto.SHA256},
	"SHA384":    {sha512.New384, crypto.SHA384},
	"SHA512":    {sha512.New, crypto.SHA512},
	"SHA3-224":  {func() hash.Hash { return sha3.New224() }, crypto.SHA3_224},
	"SHA3-256":  {func() hash.Hash { return sha3.New256() }, crypto.SHA3_256},
	"SHA3-384":  {func() hash.Hash { return sha3.New384() }, crypto.SHA3_384},
	"SHA3-512":  {func() hash.Hash { return sha3.New512() }, crypto.SHA3_512},
	"RIPEMD160": {ripemd160.New, crypto.RIPEMD160},
}

// lookupHash accepts n8n's names (SHA256, SHA3-256) and Node's getHashes()
// spellings used by Sign (sha256, RSA-SHA256, sha3-256, ripemd160).
func lookupHash(name string) (hashType, error) {
	key := strings.ToUpper(strings.TrimSpace(name))
	key = strings.TrimPrefix(key, "RSA-")
	key = strings.NewReplacer("SHA-", "SHA", "RIPEMD-160", "RIPEMD160", "RMD160", "RIPEMD160").Replace(key)
	if h, ok := hashTypes[key]; ok {
		return h, nil
	}
	if strings.HasPrefix(key, "SHA3") && !strings.HasPrefix(key, "SHA3-") {
		key = "SHA3-" + strings.TrimPrefix(key, "SHA3")
	}
	if h, ok := hashTypes[key]; ok {
		return h, nil
	}
	return hashType{}, fmt.Errorf("unsupported algorithm %q", name)
}

// PerItemConfigFields keeps "value" unresolved so each item gets its own.
func (n *CryptoNode) PerItemConfigFields() []string { return []string{"value"} }

func (n *CryptoNode) executeAction(input workflow.NodeInput, config map[string]interface{}, action string) ([]workflow.NodeOutput, error) {
	fail := func(err error) ([]workflow.NodeOutput, error) {
		return nil, fmt.Errorf("data.crypto %s: %w", action, err)
	}
	dest := cfgString(config, "data_property_name", "data")
	encoding := cfgString(config, "encoding", "hex")
	if encoding != "hex" && encoding != "base64" {
		return fail(fmt.Errorf("encoding: unknown value %q (want hex or base64)", encoding))
	}

	var keyed cryptoKeys
	switch action {
	case "hmac":
		keyed.secret = cfgString(config, "secret", "")
		if keyed.secret == "" {
			return fail(fmt.Errorf("secret is required"))
		}
	case "sign":
		k, err := parsePrivateKey(cfgString(config, "private_key", ""))
		if err != nil {
			return fail(fmt.Errorf("private_key: %w", err))
		}
		keyed.signer = k
	case "encrypt", "decrypt":
		if err := keyed.loadCipherKeys(config, action); err != nil {
			return fail(err)
		}
	}

	eval := newItemEvaluator(input)
	out := make([]workflow.Item, 0, len(input.Items))
	for idx, item := range input.Items {
		raw, err := eval.value(config["value"], item)
		if err != nil {
			return fail(fmt.Errorf("item %d: value: %w", idx, err))
		}
		value := ""
		if raw != nil {
			value = fmt.Sprintf("%v", raw)
		}
		res, err := n.actionValue(action, config, item, value, encoding, keyed)
		if err != nil {
			return fail(fmt.Errorf("item %d: %w", idx, err))
		}
		j := deepCopyMap(item.JSON)
		setPath(j, dest, res, false)
		out = append(out, workflow.Item{JSON: j, Binary: item.Binary})
	}
	return []workflow.NodeOutput{{Handle: "main", Items: out}}, nil
}

func (n *CryptoNode) actionValue(action string, config map[string]interface{}, item workflow.Item, value, encoding string, keyed cryptoKeys) (interface{}, error) {
	switch action {
	case "hash", "hmac":
		ht, err := lookupHash(cfgString(config, "type", "SHA256"))
		if err != nil {
			return nil, fmt.Errorf("type: %w", err)
		}
		h := ht.newHash()
		if action == "hmac" {
			h = hmac.New(ht.newHash, []byte(keyed.secret))
		}
		if cfgBool(config, "binary_data", false) {
			data, _, err := readBinary(item, cfgString(config, "binary_property_name", defaultBinaryProperty))
			if err != nil {
				return nil, err
			}
			h.Write(data)
		} else {
			h.Write([]byte(value))
		}
		return encodeHash(h.Sum(nil), encoding), nil

	case "sign":
		ht, err := lookupHash(cfgString(config, "algorithm", "RSA-SHA256"))
		if err != nil {
			return nil, fmt.Errorf("algorithm: %w", err)
		}
		sig, err := signDigest(keyed.signer, ht, []byte(value))
		if err != nil {
			return nil, err
		}
		return encodeHash(sig, encoding), nil

	case "generate":
		return generateRandom(config)

	case "encrypt":
		return keyed.encrypt(config, value)

	case "decrypt":
		return keyed.decrypt(config, value)
	}
	return nil, fmt.Errorf("unknown operation %q", action)
}

var nonWord = regexp.MustCompile(`\W`)

const printableASCII = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!#$%&()*+,-./:;<=>?@[]^_{|}~"

// generateRandom produces a UUID or a random string of string_length
// characters in the chosen encoding_type (ascii, base64, hex, uuid).
func generateRandom(config map[string]interface{}) (string, error) {
	kind := cfgString(config, "encoding_type", "uuid")
	if kind == "uuid" {
		return uuid.New().String(), nil
	}
	length, err := cfgInt(config, "string_length", 32)
	if err != nil {
		return "", err
	}
	if length < 1 || length > 1<<20 {
		return "", fmt.Errorf("string_length: must be between 1 and 1048576, got %d", length)
	}
	switch kind {
	case "ascii":
		var b strings.Builder
		max := big.NewInt(int64(len(printableASCII)))
		for i := 0; i < length; i++ {
			k, err := rand.Int(rand.Reader, max)
			if err != nil {
				return "", err
			}
			b.WriteByte(printableASCII[k.Int64()])
		}
		return b.String(), nil
	case "hex":
		buf := make([]byte, (length+1)/2)
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		return hex.EncodeToString(buf)[:length], nil
	case "base64":
		var s string
		for len(s) < length {
			buf := make([]byte, length)
			if _, err := rand.Read(buf); err != nil {
				return "", err
			}
			s += nonWord.ReplaceAllString(base64.StdEncoding.EncodeToString(buf), "")
		}
		return s[:length], nil
	}
	return "", fmt.Errorf("encoding_type: unknown value %q (want ascii, base64, hex or uuid)", kind)
}

// signDigest signs data with an RSA (PKCS#1 v1.5) or ECDSA (ASN.1) key, or
// Ed25519 (which signs the message itself and ignores the algorithm).
func signDigest(key crypto.Signer, ht hashType, data []byte) ([]byte, error) {
	if _, ok := key.Public().(ed25519.PublicKey); ok {
		return key.Sign(rand.Reader, data, crypto.Hash(0))
	}
	h := ht.newHash()
	h.Write(data)
	return key.Sign(rand.Reader, h.Sum(nil), ht.id)
}
