package database

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	redis "github.com/redis/go-redis/v9"
)

// Parse redis-cli style arguments, never shell commands. Escapes preserve
// binary bytes; unbalanced quotes and multiple-line input fail before I/O.
func redisCommandArgs(command string) ([]string, error) {
	if len(command) > 64<<10 {
		return nil, errors.New("Redis console command exceeds 64 KiB")
	}
	if strings.ContainsAny(command, "\r\n\x00") {
		return nil, errors.New("execute one Redis command per request; encode control bytes with escapes")
	}
	var args []string
	var word strings.Builder
	quote := byte(0)
	started := false
	for i := 0; i < len(command); i++ {
		b := command[i]
		if b == '\\' {
			i++
			if i >= len(command) {
				return nil, errors.New("unfinished Redis command escape")
			}
			started = true
			b = command[i]
			switch b {
			case 'n':
				word.WriteByte('\n')
			case 'r':
				word.WriteByte('\r')
			case 't':
				word.WriteByte('\t')
			case 'x':
				if i+2 >= len(command) {
					return nil, errors.New("invalid Redis hexadecimal escape")
				}
				n, e := strconv.ParseUint(command[i+1:i+3], 16, 8)
				if e != nil {
					return nil, errors.New("invalid Redis hexadecimal escape")
				}
				word.WriteByte(byte(n))
				i += 2
			default:
				word.WriteByte(b)
			}
			continue
		}
		if quote != 0 {
			if b == quote {
				quote = 0
			} else {
				word.WriteByte(b)
			}
			continue
		}
		if b == '\'' || b == '"' {
			quote = b
			started = true
			continue
		}
		if b == ' ' || b == '\t' {
			if started {
				args = append(args, word.String())
				word.Reset()
				started = false
			}
			continue
		}
		started = true
		word.WriteByte(b)
	}
	if quote != 0 {
		return nil, errors.New("unclosed Redis command quote")
	}
	if started {
		args = append(args, word.String())
	}
	if len(args) == 0 {
		return nil, errors.New("Redis command is required")
	}
	if len(args) > 1024 {
		return nil, errors.New("Redis command exceeds 1024 arguments")
	}
	args[0] = strings.ToUpper(args[0])
	return args, nil
}

func redisCommandSafety(args []string) (string, error) {
	// Exact command allowlists avoid classifying new/module commands as reads.
	reads := " PING ECHO TIME INFO DBSIZE TYPE EXISTS TTL PTTL EXPIRETIME PEXPIRETIME GET MGET GETRANGE STRLEN GETBIT BITCOUNT HGET HMGET HEXISTS HLEN HSTRLEN LLEN LINDEX LRANGE SCARD SISMEMBER SMISMEMBER ZCARD ZCOUNT ZLEXCOUNT ZSCORE ZMSCORE ZRANK ZREVRANK ZRANGE ZREVRANGE SCAN HSCAN SSCAN ZSCAN "
	writes := " SET SETNX SETEX PSETEX MSET MSETNX INCR INCRBY INCRBYFLOAT DECR DECRBY APPEND SETRANGE SETBIT GETDEL GETEX HSET HSETNX HDEL HINCRBY HINCRBYFLOAT LPUSH LPUSHX RPUSH RPUSHX LSET LTRIM LREM LPOP RPOP SADD SREM SPOP ZADD ZREM ZINCRBY ZPOPMIN ZPOPMAX DEL UNLINK RENAMENX EXPIRE PEXPIRE EXPIREAT PEXPIREAT PERSIST "
	cmd := args[0]
	safety := ""
	if strings.Contains(reads, " "+cmd+" ") {
		safety = "allowed"
	} else if strings.Contains(writes, " "+cmd+" ") {
		safety = "write"
	} else {
		return "", unsupported("Redis console command " + cmd)
	}
	if cmd == "DEL" || cmd == "UNLINK" {
		safety = "confirm"
	}
	// COUNT options and rank ranges are bounded before sending. The wire reader
	// also caps actual replies because compact Redis encodings may ignore COUNT.
	switch cmd {
	case "SCAN", "HSCAN", "SSCAN", "ZSCAN":
		offset := 2
		if cmd != "SCAN" {
			offset = 3
		}
		if len(args) < offset {
			return "", errors.New("Redis scan cursor is required")
		}
		if _, e := strconv.ParseUint(args[offset-1], 10, 64); e != nil {
			return "", errors.New("invalid Redis scan cursor")
		}
		for i := offset; i < len(args); i += 2 {
			if i+1 >= len(args) {
				return "", errors.New("Redis scan options require values")
			}
			switch strings.ToUpper(args[i]) {
			case "MATCH":
			case "TYPE":
				if cmd != "SCAN" {
					return "", errors.New("TYPE is only supported by SCAN")
				}
			case "COUNT":
				n, e := strconv.Atoi(args[i+1])
				if e != nil || n < 1 || n > redisPageLimit {
					return "", errors.New("Redis COUNT must be between 1 and 500")
				}
			default:
				return "", errors.New("unsupported Redis scan option")
			}
		}
	case "LRANGE", "ZRANGE", "ZREVRANGE":
		if len(args) < 4 || len(args) > 5 {
			return "", errors.New("use a bounded numeric Redis rank range")
		}
		start, e1 := strconv.ParseInt(args[2], 10, 64)
		end, e2 := strconv.ParseInt(args[3], 10, 64)
		if e1 != nil || e2 != nil || start < 0 || end < start || end-start >= redisPageLimit {
			return "", errors.New("Redis range must contain at most 500 nonnegative ranks")
		}
		if len(args) == 5 && (cmd == "LRANGE" || !strings.EqualFold(args[4], "WITHSCORES")) {
			return "", errors.New("unsupported Redis range option")
		}
	case "GETRANGE":
		if len(args) != 4 {
			return "", errors.New("GETRANGE requires key, start and end")
		}
		start, e1 := strconv.ParseInt(args[2], 10, 64)
		end, e2 := strconv.ParseInt(args[3], 10, 64)
		if e1 != nil || e2 != nil || start < 0 || end < start || end-start >= redisRawLimit {
			return "", errors.New("GETRANGE must contain at most 8 MiB using nonnegative offsets")
		}
	case "LPOP", "RPOP", "SPOP", "ZPOPMIN", "ZPOPMAX":
		if len(args) < 2 || len(args) > 3 {
			return "", errors.New("Redis pop requires a key and optional bounded count")
		}
		if len(args) == 3 {
			n, err := strconv.Atoi(args[2])
			if err != nil || n < 1 || n > redisPageLimit {
				return "", errors.New("Redis pop count must be between 1 and 500")
			}
		}
	case "INFO":
		if len(args) > 2 {
			return "", errors.New("INFO accepts one section")
		}
	}
	return safety, nil
}
func redisExecute(ctx context.Context, client *redis.Client, p map[string]any, locked bool) (any, error) {
	command, err := redisRequiredText(p, "command")
	if err != nil {
		return nil, err
	}
	args, err := redisCommandArgs(command)
	if err != nil {
		return nil, err
	}
	safety, err := redisCommandSafety(args)
	if err != nil {
		return nil, err
	}
	if locked && safety != "allowed" {
		return nil, errors.New("writes are locked for this read-only or production connection")
	}
	argv := make([]any, len(args))
	for i, a := range args {
		argv[i] = a
	}
	value, err := client.Do(ctx, argv...).Result()
	if errors.Is(err, redis.Nil) {
		value = nil
		err = nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"command": command, "safety": safety, "value": redisConsoleValue(value)}, nil
}
func redisConsoleValue(value any) any {
	switch v := value.(type) {
	case int64:
		if v > 9007199254740991 || v < -9007199254740991 {
			return strconv.FormatInt(v, 10)
		}
		return v
	case string:
		return redisDisplay(v)
	case []byte:
		return redisDisplay(string(v))
	case []any:
		for i, a := range v {
			v[i] = redisConsoleValue(a)
		}
		return v
	case nil:
		return nil
	default:
		return fmt.Sprint(v)
	}
}
