package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var ErrNotFound = errors.New("not found")

type Store interface {
	Load(context.Context, string, any) error
	Atomic(context.Context, map[string]any) error
	Keys(context.Context, string) ([]string, error)
}

func encode(v any) ([]byte, error) {
	if p, ok := v.(proto.Message); ok {
		return protojson.Marshal(p)
	}
	return json.Marshal(v)
}
func decode(b []byte, v any) error {
	if p, ok := v.(proto.Message); ok {
		return protojson.Unmarshal(b, p)
	}
	return json.Unmarshal(b, v)
}

// File is a fsync+rename development store; production/lab use Redis AOF always.
type File struct {
	mu   sync.Mutex
	path string
	kv   map[string]json.RawMessage
}

func NewFile(path string) (*File, error) {
	s := &File{path: path, kv: map[string]json.RawMessage{}}
	if path != "" {
		b, e := os.ReadFile(path)
		if e == nil {
			if e = json.Unmarshal(b, &s.kv); e != nil {
				return nil, e
			}
		} else if !os.IsNotExist(e) {
			return nil, e
		}
	}
	return s, nil
}
func (s *File) Load(ctx context.Context, k string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.kv[k]
	if !ok {
		return ErrNotFound
	}
	return decode(b, v)
}
func (s *File) Atomic(ctx context.Context, vs map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := map[string]json.RawMessage{}
	for k, v := range s.kv {
		next[k] = v
	}
	for k, v := range vs {
		if v == nil {
			delete(next, k)
			continue
		}
		b, e := encode(v)
		if e != nil {
			return e
		}
		next[k] = b
	}
	if s.path != "" {
		if e := os.MkdirAll(filepath.Dir(s.path), 0700); e != nil {
			return e
		}
		b, e := json.Marshal(next)
		if e != nil {
			return e
		}
		f, e := os.CreateTemp(filepath.Dir(s.path), ".state-*")
		if e != nil {
			return e
		}
		tmp := f.Name()
		defer os.Remove(tmp)
		if _, e = f.Write(b); e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
		if e = os.Rename(tmp, s.path); e != nil {
			return e
		}
		d, e := os.Open(filepath.Dir(s.path))
		if e == nil {
			e = d.Sync()
			d.Close()
		}
		if e != nil {
			return e
		}
	}
	s.kv = next
	return nil
}
func (s *File) Keys(ctx context.Context, p string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for k := range s.kv {
		if strings.HasPrefix(k, p) {
			out = append(out, k)
		}
	}
	return out, nil
}

type Redis struct {
	c      *redis.Client
	prefix string
}

func NewRedis(ctx context.Context, addr, prefix string) (*Redis, error) {
	s := &Redis{redis.NewClient(&redis.Options{Addr: addr}), prefix}
	if e := s.c.Ping(ctx).Err(); e != nil {
		return nil, e
	}
	return s, nil
}
func (s *Redis) Load(ctx context.Context, k string, v any) error {
	b, e := s.c.Get(ctx, s.prefix+k).Bytes()
	if errors.Is(e, redis.Nil) {
		return ErrNotFound
	}
	if e != nil {
		return e
	}
	return decode(b, v)
}
func (s *Redis) Atomic(ctx context.Context, vs map[string]any) error {
	_, e := s.c.TxPipelined(ctx, func(p redis.Pipeliner) error {
		for k, v := range vs {
			if v == nil {
				p.Del(ctx, s.prefix+k)
				continue
			}
			b, e := encode(v)
			if e != nil {
				return e
			}
			p.Set(ctx, s.prefix+k, b, 0)
		}
		return nil
	})
	return e
}
func (s *Redis) Keys(ctx context.Context, p string) ([]string, error) {
	var cursor uint64
	var out []string
	seen := map[string]bool{}
	for {
		ks, n, e := s.c.Scan(ctx, cursor, s.prefix+p+"*", 100).Result()
		if e != nil {
			return nil, e
		}
		for _, k := range ks {
			if !seen[k] {
				out = append(out, strings.TrimPrefix(k, s.prefix))
				seen[k] = true
			}
		}
		cursor = n
		if cursor == 0 {
			break
		}
	}
	return out, nil
}
