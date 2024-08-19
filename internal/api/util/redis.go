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

// StoreNodeLog stores a log entry as a string and publishes it to a Redis channel.
func StoreNodeLog(enclaveName string, serviceName string, logEntry string) error {
	redisKey := "node-logs:" + serviceName + "-" + enclaveName
	redisChannel := "log-channel:" + serviceName + "-" + enclaveName

	// Store the log in a Redis list
	err := redisClient.RPush(ctx, redisKey, logEntry).Err()
	if err != nil {
		return err
	}

	// Publish the log to the Redis channel for real-time subscription
	err = redisClient.Publish(ctx, redisChannel, logEntry).Err()
	return err
}

// GetNodeLogs retrieves logs from Redis within the specified range.
func GetNodeLogs(enclaveName string, serviceName string, start, end int64) ([]string, error) {
	redisKey := "node-logs:" + serviceName + "-" + enclaveName
	logs, err := redisClient.LRange(ctx, redisKey, start, end).Result()
	if err != nil {
		return nil, err
	}

	return logs, nil
}

// SubscribeToLogs subscribes to real-time logs for a service.
func SubscribeToLogs(enclaveName string, serviceName string, handleLog func(logEntry string)) error {
	redisChannel := "log-channel:" + serviceName + "-" + enclaveName
	pubsub := redisClient.Subscribe(ctx, redisChannel)

	// Wait for confirmation that subscription is created before publishing anything.
	_, err := pubsub.Receive(ctx)
	if err != nil {
		return err
	}

	// Go channel which receives messages.
	ch := pubsub.Channel()

	// Consume messages.
	go func() {
		for msg := range ch {
			redisKey := "node-logs:" + serviceName + "-" + enclaveName
			index, err := redisClient.LPos(ctx, redisKey, msg.Payload, redis.LPosArgs{}).Result()
			if err != nil {
				continue
			}

			// Create a JSON object with the index and log entry
			logEntryWithIndex := map[string]interface{}{
				"index": index,
				"log":   msg.Payload,
			}

			// Convert the JSON object to a string
			logEntryWithIndexJSON, err := json.Marshal(logEntryWithIndex)
			if err != nil {
				continue
			}

			// Pass the JSON string to the handler
			handleLog(string(logEntryWithIndexJSON))
		}
	}()

	return nil
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
