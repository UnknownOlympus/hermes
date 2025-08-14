package server

import (
	"context"
	"time"

	"google.golang.org/protobuf/proto"
)

//nolint:ireturn // This function is intended to return a generic interface.
func getCachedOrFetch[T proto.Message](
	ctx context.Context,
	serv *Server,
	opName, cacheKey string,
	cacheTTL time.Duration,
	fetchFn func() (T, error),
	newFn func() T,
) (T, error) {
	var empty T

	// search in redis.
	cachedData, err := serv.redis.Get(ctx, cacheKey).Bytes()
	if err == nil {
		serv.log.DebugContext(ctx, "Cache HIT", "op", opName, "key", cacheKey)
		serv.metrics.CacheOps.WithLabelValues("get", "hit").Inc()

		response := newFn()
		err = proto.Unmarshal(cachedData, response)
		if err == nil {
			return response, nil
		}
		serv.log.ErrorContext(ctx, "Failed to unmarshal cached data", "op", opName, "error", err)
	} else {
		serv.log.DebugContext(ctx, "Cache MISS", "op", opName, "key", cacheKey)
		serv.metrics.CacheOps.WithLabelValues("get", "miss").Inc()
	}

	// If it is not in the cache, we call the passed function to retrieve the data.
	freshData, err := fetchFn()
	if err != nil {
		return empty, err
	}

	// Stores fresh data in the cache.
	serializedData, err := proto.Marshal(freshData)
	if err != nil {
		serv.log.ErrorContext(ctx, "Failed to marshal response for caching", "op", opName, "error", err)
	} else {
		err = serv.redis.Set(ctx, cacheKey, serializedData, cacheTTL).Err()
		if err != nil {
			serv.metrics.CacheOps.WithLabelValues("set", "error").Inc()
			serv.log.ErrorContext(ctx, "Failed to save data to cache", "op", opName, "error", err)
		} else {
			serv.metrics.CacheOps.WithLabelValues("set", "success").Inc()
		}
	}

	return freshData, nil
}
