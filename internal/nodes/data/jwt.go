package data

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

// JWTNode signs, decodes and verifies JSON Web Tokens.
// Type: "data.jwt" (n8n: JWT).
//
// Config:
//
//	operation         sign | decode | verify
//	key_type          passphrase (HS*) | pem_key (RS*/PS*/ES*)
//	secret            HMAC secret (key_type=passphrase)
//	private_key       PEM key for signing; public_key: PEM key for verifying
//	algorithm         HS256|HS384|HS512|RS256|RS384|RS512|PS256|PS384|PS512|ES256|ES384|ES512
//	use_json          sign: take the payload from claims_json instead of claims
//	claims            sign: {audience, expires_in, issuer, jwtid, not_before, subject} plus any custom claims
//	claims_json       sign: payload object (JSON)
//	header_claims     sign: extra header fields (JSON object); kid: key id header
//	token             decode/verify: the token (evaluated per item)
//	complete          decode/verify: output {header, payload, signature} instead of {payload}
//	ignore_expiration, ignore_not_before, clock_tolerance (seconds): verify options
type JWTNode struct {
	now func() time.Time
}

func (n *JWTNode) Type() string { return "data.jwt" }

// PublishesToThirdParty: data.jwt sends nothing off this machine (issue #1418).
func (JWTNode) PublishesToThirdParty() bool { return false }

// PerItemConfigFields keeps per-item values unresolved for Execute.
func (n *JWTNode) PerItemConfigFields() []string { return []string{"token", "claims_json"} }

func (n *JWTNode) clock() time.Time {
	if n.now != nil {
		return n.now()
	}
	return time.Now()
}

func (n *JWTNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	op := cfgString(config, "operation", "sign")
	fail := func(err error) ([]workflow.NodeOutput, error) {
		return nil, fmt.Errorf("data.jwt %s: %w", op, err)
	}
	alg := strings.ToUpper(cfgString(config, "algorithm", "HS256"))
	var keys jwtKeys
	if op == "sign" || op == "verify" {
		k, err := loadJWTKeys(config, alg, op)
		if err != nil {
			return fail(err)
		}
		keys = k
	} else if op != "decode" {
		return nil, fmt.Errorf("data.jwt: unknown operation %q (want sign, decode or verify)", op)
	}

	eval := newItemEvaluator(input)
	out := make([]workflow.Item, 0, len(input.Items))
	for idx, item := range input.Items {
		var res map[string]interface{}
		var err error
		switch op {
		case "sign":
			res, err = n.sign(config, eval, item, alg, keys)
		default:
			var raw interface{}
			raw, err = eval.value(config["token"], item)
			if err == nil {
				token := strings.TrimSpace(fmt.Sprintf("%v", raw))
				if raw == nil || token == "" {
					err = fmt.Errorf("token is required")
				} else if op == "decode" {
					res, err = decodeJWT(token, cfgBool(config, "complete", false))
				} else {
					res, err = n.verify(config, token, alg, keys)
				}
			}
		}
		if err != nil {
			return fail(fmt.Errorf("item %d: %w", idx, err))
		}
		out = append(out, workflow.Item{JSON: res})
	}
	return []workflow.NodeOutput{{Handle: "main", Items: out}}, nil
}

func (n *JWTNode) sign(config map[string]interface{}, eval itemEvaluator, item workflow.Item, alg string, keys jwtKeys) (map[string]interface{}, error) {
	now := n.clock().Unix()
	payload := map[string]interface{}{}
	if cfgBool(config, "use_json", false) {
		raw, err := eval.value(config["claims_json"], item)
		if err != nil {
			return nil, fmt.Errorf("claims_json: %w", err)
		}
		obj, err := cfgObject(map[string]interface{}{"claims_json": raw}, "claims_json")
		if err != nil {
			return nil, err
		}
		for k, v := range obj {
			payload[k] = v
		}
	} else {
		claims, err := cfgObject(config, "claims")
		if err != nil {
			return nil, err
		}
		registered := map[string]string{"audience": "aud", "issuer": "iss", "jwtid": "jti", "subject": "sub"}
		for k, v := range claims {
			switch k {
			case "expires_in", "expiresIn":
				if f, ok := toNumber(v); ok {
					payload["exp"] = now + int64(f)
				}
			case "not_before", "notBefore":
				if f, ok := toNumber(v); ok {
					payload["nbf"] = now + int64(f)
				}
			default:
				if std, ok := registered[k]; ok {
					if !isEmptyValue(v) {
						payload[std] = v
					}
					continue
				}
				payload[k] = v
			}
		}
	}
	if _, ok := payload["iat"]; !ok {
		payload["iat"] = now
	}
	header := map[string]interface{}{"alg": alg, "typ": "JWT"}
	if kid := cfgString(config, "kid", ""); kid != "" {
		header["kid"] = kid
	}
	extra, err := cfgObject(config, "header_claims")
	if err != nil {
		return nil, err
	}
	for k, v := range extra {
		header[k] = v
	}
	header["alg"] = alg
	token, err := encodeJWT(header, payload, alg, keys)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"token": token}, nil
}

func (n *JWTNode) verify(config map[string]interface{}, token, alg string, keys jwtKeys) (map[string]interface{}, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("jwt malformed: expected 3 dot-separated parts")
	}
	full, err := decodeJWT(token, true)
	if err != nil {
		return nil, err
	}
	header, _ := full["header"].(map[string]interface{})
	if got, _ := header["alg"].(string); got != alg {
		return nil, fmt.Errorf("invalid algorithm: token uses %q, expected %q", got, alg)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("invalid signature encoding: %w", err)
	}
	if err := verifyJWTSignature(alg, keys, []byte(parts[0]+"."+parts[1]), sig); err != nil {
		return nil, fmt.Errorf("the JWT token can't be verified (check the key and algorithm): %w", err)
	}
	payload, _ := full["payload"].(map[string]interface{})
	tolerance, err := cfgFloat(config, "clock_tolerance", 0)
	if err != nil {
		return nil, err
	}
	now := float64(n.clock().Unix())
	if exp, ok := toNumber(payload["exp"]); ok && !cfgBool(config, "ignore_expiration", false) && now >= exp+tolerance {
		return nil, fmt.Errorf("jwt expired at %s", time.Unix(int64(exp), 0).UTC().Format(time.RFC3339))
	}
	if nbf, ok := toNumber(payload["nbf"]); ok && !cfgBool(config, "ignore_not_before", false) && now+tolerance < nbf {
		return nil, fmt.Errorf("jwt not active until %s", time.Unix(int64(nbf), 0).UTC().Format(time.RFC3339))
	}
	if cfgBool(config, "complete", false) {
		return full, nil
	}
	return map[string]interface{}{"payload": payload}, nil
}

func decodeJWT(token string, complete bool) (map[string]interface{}, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("jwt malformed: expected 3 dot-separated parts")
	}
	decodePart := func(name, s string) (interface{}, error) {
		b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
		if err != nil {
			return nil, fmt.Errorf("jwt malformed: %s is not base64url: %w", name, err)
		}
		var v interface{}
		if err := json.Unmarshal(b, &v); err != nil {
			if name == "payload" {
				return string(b), nil
			}
			return nil, fmt.Errorf("jwt malformed: %s is not JSON: %w", name, err)
		}
		return v, nil
	}
	payload, err := decodePart("payload", parts[1])
	if err != nil {
		return nil, err
	}
	if !complete {
		return map[string]interface{}{"payload": payload}, nil
	}
	header, err := decodePart("header", parts[0])
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"header": header, "payload": payload, "signature": parts[2]}, nil
}
