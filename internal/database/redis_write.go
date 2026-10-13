package database

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	redis "github.com/redis/go-redis/v9"
)

// Scripts below are fixed application operations. User-supplied EVAL/FCALL is
// never accepted by the console, including when skipSafetyCheck is set.
const redisHashUpdateScript = `
local old = ARGV[1]
local new = ARGV[2]
if ARGV[4] == 'update' then
 if redis.call('HEXISTS', KEYS[1], old) == 0 then return redis.error_reply('Hash field no longer exists') end
 if old ~= new and redis.call('HEXISTS', KEYS[1], new) == 1 then return redis.error_reply('Target hash field already exists') end
end
local expiration = redis.pcall('HPEXPIRETIME', KEYS[1], 'FIELDS', 1, old)
if type(expiration) == 'table' and expiration.err and not string.find(string.lower(expiration.err), 'unknown command', 1, true) then return expiration end
if not redis.acl_check_cmd('HSET', KEYS[1], new, ARGV[3]) then return redis.error_reply('NOPERM HSET denied') end
if old ~= new and not redis.acl_check_cmd('HDEL', KEYS[1], old) then return redis.error_reply('NOPERM HDEL denied') end
if tonumber(ARGV[5]) > 0 and not redis.acl_check_cmd('EXPIRE', KEYS[1], ARGV[5]) then return redis.error_reply('NOPERM EXPIRE denied') end
if type(expiration) == 'table' and not expiration.err and expiration[1] > 0 and not redis.acl_check_cmd('HPEXPIREAT', KEYS[1], expiration[1], 'FIELDS', 1, new) then return redis.error_reply('NOPERM HPEXPIREAT denied') end
redis.call('HSET', KEYS[1], new, ARGV[3])
if type(expiration) == 'table' and not expiration.err and expiration[1] > 0 then redis.call('HPEXPIREAT', KEYS[1], expiration[1], 'FIELDS', 1, new) end
if old ~= new then redis.call('HDEL', KEYS[1], old) end
if tonumber(ARGV[5]) > 0 then redis.call('EXPIRE', KEYS[1], ARGV[5]) end
return 1`

func redisWrite(ctx context.Context, client *redis.Client, route, key string, p map[string]any) (any, error) {
	ttl, err := redisInt(p, "ttl", 0)
	if err != nil {
		return nil, err
	}
	if ttl > 3155760000 {
		return nil, errors.New("TTL exceeds the 100-year limit")
	}
	var args []any
	switch route {
	case "set-string":
		value, e := redisRequiredText(p, "value")
		if e != nil {
			return nil, e
		}
		args = []any{"SET", key, value}
		if p["ttl"] == nil {
			args = append(args, "KEEPTTL")
		} else if ttl > 0 {
			args = append(args, "EX", ttl)
		}
	case "delete-key":
		args = []any{"DEL", key}
	case "rename-key":
		dest, e := redisKey(p, "newKeyRaw")
		if e != nil {
			return nil, e
		}
		changed, e := client.RenameNX(ctx, key, dest).Result()
		if e != nil {
			return nil, e
		}
		if !changed {
			return nil, errors.New("target key already exists")
		}
		return nil, nil
	case "hash-set", "hash-field-update":
		fieldName := "field"
		if route == "hash-field-update" {
			fieldName = "oldField"
		}
		field, e := redisRequiredText(p, fieldName)
		if e != nil {
			return nil, e
		}
		dest := field
		mode := "set"
		if route == "hash-field-update" {
			dest, e = redisRequiredText(p, "newField")
			if e != nil {
				return nil, e
			}
			mode = "update"
		}
		value, e := redisRequiredText(p, "value")
		if e != nil {
			return nil, e
		}
		return nil, client.Eval(ctx, redisHashUpdateScript, []string{key}, field, dest, value, mode, ttl).Err()
	case "hash-del":
		field, e := redisRequiredText(p, "field")
		if e != nil {
			return nil, e
		}
		args = []any{"HDEL", key, field}
	case "list-push", "list-set":
		value, e := redisRequiredText(p, "value")
		if e != nil {
			return nil, e
		}
		if route == "list-push" {
			args = []any{"RPUSH", key, value}
		} else {
			index, e := redisInt(p, "index", 0)
			if e != nil {
				return nil, e
			}
			args = []any{"LSET", key, index, value}
		}
	case "list-remove":
		index, e := redisInt(p, "index", 0)
		if e != nil {
			return nil, e
		}
		marker := make([]byte, 32)
		if _, e = rand.Read(marker); e != nil {
			return nil, e
		}
		script := `if redis.call('LINDEX',KEYS[1],ARGV[1]) == false then return redis.error_reply('List index out of range') end
if redis.call('LPOS',KEYS[1],ARGV[2]) ~= false then return redis.error_reply('Concurrent list deletion marker collision; retry') end
if not redis.acl_check_cmd('LSET',KEYS[1],ARGV[1],ARGV[2]) or not redis.acl_check_cmd('LREM',KEYS[1],1,ARGV[2]) then return redis.error_reply('NOPERM list edit denied') end
redis.call('LSET',KEYS[1],ARGV[1],ARGV[2]); return redis.call('LREM',KEYS[1],1,ARGV[2])`
		return nil, client.Eval(ctx, script, []string{key}, index, "dbx-delete-"+hex.EncodeToString(marker)).Err()
	case "set-add", "set-remove", "zadd", "zrem":
		member, e := redisRequiredText(p, "member")
		if e != nil {
			return nil, e
		}
		cmd := map[string]string{"set-add": "SADD", "set-remove": "SREM", "zadd": "ZADD", "zrem": "ZREM"}[route]
		args = []any{cmd, key}
		if route == "zadd" {
			score, e := redisScore(str(p, "score"))
			if e != nil {
				return nil, e
			}
			args = append(args, score)
		}
		args = append(args, member)
	case "zset-update":
		original, e := redisRequiredText(p, "originalMember")
		if e != nil {
			return nil, e
		}
		member, e := redisRequiredText(p, "member")
		if e != nil {
			return nil, e
		}
		score, e := redisScore(str(p, "score"))
		if e != nil {
			return nil, e
		}
		expected, e := redisScore(str(p, "expectedScore"))
		if e != nil {
			return nil, e
		}
		script := `local score=redis.call('ZSCORE',KEYS[1],ARGV[1]); if score == false then return 0 end
if score ~= ARGV[3] and (tonumber(score) == nil or tonumber(score) ~= tonumber(ARGV[3])) then return redis.error_reply('Sorted-set score changed; reload before saving') end
if ARGV[1] ~= ARGV[2] and redis.call('ZSCORE',KEYS[1],ARGV[2]) ~= false then return redis.error_reply('Target sorted-set member already exists') end
if not redis.acl_check_cmd('ZADD',KEYS[1],ARGV[4],ARGV[2]) then return redis.error_reply('NOPERM ZADD denied') end
if ARGV[1] ~= ARGV[2] and not redis.acl_check_cmd('ZREM',KEYS[1],ARGV[1]) then return redis.error_reply('NOPERM ZREM denied') end
redis.call('ZADD',KEYS[1],ARGV[4],ARGV[2]); if ARGV[1] ~= ARGV[2] then redis.call('ZREM',KEYS[1],ARGV[1]) end; return 1`
		n, e := client.Eval(ctx, script, []string{key}, original, member, redisScoreText(expected), redisScoreText(score)).Int64()
		if e != nil {
			return nil, e
		}
		if n != 1 {
			return nil, errors.New("sorted-set member no longer exists; reload before saving")
		}
		return false, nil
	case "set-ttl", "set-expire-at":
		args, err = redisExpiryArgs(route, key, p)
		if err != nil {
			return nil, err
		}
	default:
		return nil, unsupported("Redis " + route)
	}
	// Apply collection insertion and expiry as one operation.
	if ttl > 0 && (route == "list-push" || route == "set-add" || route == "zadd") {
		return nil, client.Eval(ctx, `if not redis.acl_check_cmd(ARGV[1],KEYS[1],unpack(ARGV,3)) or not redis.acl_check_cmd('EXPIRE',KEYS[1],ARGV[2]) then return redis.error_reply('NOPERM collection update or expiry denied') end
local result=redis.call(ARGV[1],KEYS[1],unpack(ARGV,3)); redis.call('EXPIRE',KEYS[1],ARGV[2]); return result`, []string{key}, append([]any{args[0], ttl}, args[2:]...)...).Err()
	}
	return nil, client.Do(ctx, args...).Err()
}

func redisScore(s string) (float64, error) {
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(n) {
		return 0, errors.New("sorted-set score must be a number or positive/negative infinity")
	}
	return n, nil
}
func redisScoreText(score float64) string {
	if math.IsInf(score, 1) {
		return "inf"
	}
	if math.IsInf(score, -1) {
		return "-inf"
	}
	return strconv.FormatFloat(score, 'g', -1, 64)
}
func redisExpiryArgs(route, key string, p map[string]any) ([]any, error) {
	if strings.HasSuffix(route, "expire-at") {
		at, err := redisInt(p, "expireAt", 0)
		if err != nil {
			return nil, err
		}
		if at < 0 || at > 253402300799 {
			return nil, errors.New("expireAt must be a valid Unix timestamp in seconds")
		}
		return []any{"EXPIREAT", key, at}, nil
	}
	ttl, err := redisInt(p, "ttl", 0)
	if err != nil {
		return nil, err
	}
	if ttl > 3155760000 {
		return nil, errors.New("TTL exceeds the 100-year limit")
	}
	if ttl <= 0 {
		return []any{"PERSIST", key}, nil
	}
	return []any{"EXPIRE", key, ttl}, nil
}
func redisBatchWrite(ctx context.Context, client *redis.Client, route string, p map[string]any) (any, error) {
	raws, ok := p["keyRaws"].([]any)
	if !ok {
		if typed, ok := p["keyRaws"].([]string); ok {
			raws = make([]any, len(typed))
			for i, v := range typed {
				raws[i] = v
			}
		} else {
			return nil, errors.New("keyRaws must be an array")
		}
	}
	if len(raws) > 1000 {
		return nil, errors.New("at most 1000 keys may be changed in one request")
	}
	keys := []string{}
	seen := map[string]bool{}
	for _, v := range raws {
		k, e := redisKey(map[string]any{"keyRaw": v}, "keyRaw")
		if e != nil {
			return nil, e
		}
		if !seen[k] {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	if len(keys) == 0 {
		if route == "delete-keys" {
			return int64(0), nil
		}
		return map[string]any{"applied": 0, "missing_key_raws": []string{}}, nil
	}
	if route == "delete-keys" {
		return client.Del(ctx, keys...).Result()
	}
	args, err := redisExpiryArgs(route, "", p)
	if err != nil {
		return nil, err
	}
	// All keys are checked and changed in one non-interleaved script, so PERSIST's
	// zero return for an already-persistent key is not mistaken for a missing key.
	script := `for _,key in ipairs(KEYS) do if not redis.acl_check_cmd('EXISTS',key) or not redis.acl_check_cmd(ARGV[1],key,unpack(ARGV,2)) then return redis.error_reply('NOPERM key expiry denied') end end
local result={}; for i,key in ipairs(KEYS) do if redis.call('EXISTS',key)==0 then result[i]=0 else redis.call(ARGV[1],key,unpack(ARGV,2)); result[i]=1 end end; return result`
	argv := []any{args[0]}
	argv = append(argv, args[2:]...)
	values, err := client.Eval(ctx, script, keys, argv...).Slice()
	if err != nil {
		return nil, err
	}
	applied := 0
	missing := []string{}
	for i, v := range values {
		if fmt.Sprint(v) == "1" {
			applied++
		} else {
			missing = append(missing, fmt.Sprint(redisBlob(keys[i])["raw_base64"]))
		}
	}
	return map[string]any{"applied": applied, "missing_key_raws": missing}, nil
}
