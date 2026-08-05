package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// rawKeyPrefix isola as chaves deste scraper no espaço de nomes do Redis.
const rawKeyPrefix = "tap:raw:"

// Redis guarda as respostas brutas da API, indexadas pela chave da busca.
type Redis struct {
	client *redis.Client
	ttl    time.Duration
}

// NewRedis conecta ao Redis e valida a conexão.
func NewRedis(ctx context.Context, addr, password string, db int, ttl time.Duration) (*Redis, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("failed to ping redis at %s: %w", addr, err)
	}
	return &Redis{client: client, ttl: ttl}, nil
}

// Close encerra a conexão.
func (r *Redis) Close() error {
	if err := r.client.Close(); err != nil {
		return fmt.Errorf("failed to close redis: %w", err)
	}
	return nil
}

// Ping verifica a conexão com o Redis.
func (r *Redis) Ping(ctx context.Context) error {
	if err := r.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("failed to ping redis: %w", err)
	}
	return nil
}

// RawKey devolve a chave sob a qual a resposta bruta de uma busca é guardada.
// O timestamp permite conservar o histórico de coletas da mesma busca.
func RawKey(searchKey string, scrapedAt time.Time) string {
	return fmt.Sprintf("%s%s:%d", rawKeyPrefix, searchKey, scrapedAt.Unix())
}

// SaveRaw grava o corpo bruto da resposta com TTL. Devolve a chave usada, que
// é referenciada na coluna searches.raw_key do PostgreSQL.
func (r *Redis) SaveRaw(ctx context.Context, searchKey string, scrapedAt time.Time, payload []byte) (string, error) {
	key := RawKey(searchKey, scrapedAt)
	if err := r.client.Set(ctx, key, payload, r.ttl).Err(); err != nil {
		return "", fmt.Errorf("failed to store raw response at %q: %w", key, err)
	}

	// Um índice ordenado por tempo permite listar as coletas de uma busca.
	indexKey := rawKeyPrefix + "index:" + searchKey
	if err := r.client.ZAdd(ctx, indexKey, redis.Z{
		Score:  float64(scrapedAt.Unix()),
		Member: key,
	}).Err(); err != nil {
		return "", fmt.Errorf("failed to index raw response %q: %w", key, err)
	}
	if err := r.client.Expire(ctx, indexKey, r.ttl).Err(); err != nil {
		return "", fmt.Errorf("failed to set ttl on index %q: %w", indexKey, err)
	}

	return key, nil
}

// LoadRaw recupera uma resposta bruta pela chave exata.
//
// Ausência é ErrNotFound, não erro genérico: com TTL de 7 dias a expiração é o
// caso normal, e a camada HTTP precisa distinguí-la de uma falha do Redis — uma
// é 404, a outra é 500.
func (r *Redis) LoadRaw(ctx context.Context, key string) ([]byte, error) {
	payload, err := r.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("%w: resposta bruta %q (expirada ou nunca gravada)", ErrNotFound, key)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load raw response %q: %w", key, err)
	}
	return payload, nil
}

// LatestRaw devolve a coleta mais recente de uma busca.
func (r *Redis) LatestRaw(ctx context.Context, searchKey string) ([]byte, error) {
	indexKey := rawKeyPrefix + "index:" + searchKey
	keys, err := r.client.ZRangeArgs(ctx, redis.ZRangeArgs{
		Key:   indexKey,
		Start: 0,
		Stop:  0,
		Rev:   true,
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to read index %q: %w", indexKey, err)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: nenhuma coleta bruta para %q", ErrNotFound, searchKey)
	}
	return r.LoadRaw(ctx, keys[0])
}
