package util

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"github.com/go-redis/redis/v8"
	"time"
)

type RedisSession struct {
	ResponseLines []string
}

var ctx = context.Background()
var redisClient *redis.Client

func Init() {
	redisClient = redis.NewClient(&redis.Options{
		Addr:         "lzero-api-redis-g0loij.serverless.euw1.cache.amazonaws.com:6379",
		Password:     "",
		DB:           0,
		DialTimeout:  10 * time.Second,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		TLSConfig: &tls.Config{
			InsecureSkipVerify: true, // Skip certificate verification if needed
		},
	})
}

func StoreSession(sessionID string, session *RedisSession) error {
	sessionData, err := json.Marshal(session)
	if err != nil {
		return err
	}
	err = redisClient.Set(ctx, sessionID, sessionData, 0).Err()
	return err
}

func GetSession(sessionID string) (*RedisSession, error) {
	sessionData, err := redisClient.Get(ctx, sessionID).Result()
	if err != nil {
		return nil, err
	}
	var session RedisSession
	err = json.Unmarshal([]byte(sessionData), &session)
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func GetRedisClient() *redis.Client {
	return redisClient
}

func GetContext() context.Context {
	return ctx
}
