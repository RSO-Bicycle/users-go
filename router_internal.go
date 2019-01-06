package main

import (
	"context"
	"database/sql"
	"github.com/go-redis/redis"
	"github.com/julienschmidt/httprouter"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"net/http"
	"strings"
	"time"
)

func createInternalRouter(db *sql.DB, client *redis.Client) *httprouter.Router {
	router := httprouter.New()

	// Create a authorize route
	router.GET("/authorize/", func(w http.ResponseWriter, req *http.Request, params httprouter.Params) {
		token := req.Header["Authorization"]
		if len(token) == 0 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		tk := strings.Split(token[0], "Bearer ")
		if len(tk) != 2 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		if res, err := client.Get("token:" + tk[1]).Result(); err != nil {
			w.WriteHeader(http.StatusUnauthorized)
		} else {
			// Res is a jwt token
			w.Header().Set("Authorization", "Bearer "+res)
			w.WriteHeader(http.StatusOK)
		}
	})
	// Create healthz route
	router.GET("/healthz", func(w http.ResponseWriter, req *http.Request, params httprouter.Params) {
		ctx, cf := context.WithTimeout(context.Background(), time.Second*2)
		defer cf()

		// Probably leaking so many resources I don't even know :D
		ch := make(chan bool, 2)
		go func() {
			ch <- db.PingContext(ctx) == nil
		}()
		go func() {
			ch <- client.Ping().Err() == nil
		}()

		b := true
		for i := 0; i < 2; i++ {
			select {
			case c := <-ch:
				b = b && c
			case <-ctx.Done():
				b = false
				break
			}
		}

		if b {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusBadGateway)
		}
	})
	// Create metricz route
	router.Handler(http.MethodGet, "/metricz", promhttp.Handler())

	return router
}
