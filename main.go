package main

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/go-redis/redis"
	_ "github.com/lib/pq"
	"github.com/mitchellh/mapstructure"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type config struct {
	Db struct {
		Host     string `yaml:"host"`
		Port     int    `yaml:"port"`
		User     string `yaml:"user"`
		Pass     string `yaml:"pass"`
		Database string `yaml:"database"`
	} `yaml:"db"`
	Cache struct {
		Host string `yaml:"host"`
		Port int    `yaml:"port"`
		Pass string `yaml:"pass"`
	} `yaml:"cache"`
	Kafka struct {
		BrokerAddr []string `yaml:"brokerAddr"`
		Topics     []struct {
			Name        string `yaml:"name"`
			PartitionID int32  `yaml:"partitionId"`
		} `yaml:"topics"`
	} `yaml:"kafka"`
}

func main() {
	// Configure the logger
	logger, err := zap.NewProduction()
	if err != nil {
		panic(err.Error())
	}
	logger.Info("logger initialized")
	// Load configuration
	v := viper.New()
	v.SetConfigName("config")
	v.SetEnvPrefix("SERVICE")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AddConfigPath(".")
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		logger.Fatal("loading config", zap.Error(err))
	}

	cfg := new(config)
	if err := v.Unmarshal(cfg, viper.DecoderConfigOption(func(m *mapstructure.DecoderConfig) {
		m.TagName = "yaml"
	})); err != nil {
		logger.Fatal("parsing config", zap.Error(err))
	}
	logger.Info("configuration loaded")

	// Connect to PostgreSQL
	dbUrl := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		cfg.Db.Host, cfg.Db.Port, cfg.Db.User, cfg.Db.Pass, cfg.Db.Database)
	db, err := sql.Open("postgres", dbUrl)
	if err != nil {
		panic(err.Error())
	}
	ctx, cf := context.WithTimeout(context.Background(), time.Second*5)
	defer cf()
	if err := db.PingContext(ctx); err != nil {
		panic(err.Error())
	}
	defer db.Close()
	logger.Info("successfully connected to PostgreSQL")

	// Connect to Redis
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Cache.Host + ":" + strconv.FormatInt(int64(cfg.Cache.Port), 10),
		Password: cfg.Cache.Pass,
		DB:       0,
	})

	if _, err := client.Ping().Result(); err != nil {
		panic(err.Error())
	}
	defer client.Close()
	logger.Info("successfully connected to Redis")

	// Create the router
	go func() {
		http.ListenAndServe("0.0.0.0:8080", createPublicRouter(logger.Named("public-router"), db, client))
	}()
	go func() {
		http.ListenAndServe("0.0.0.0:8081", createInternalRouter(db, client))
	}()
	logger.Info("Running public server on 0.0.0.0:8080")
	logger.Info("Running internal server on 0.0.0.0:8081")

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL, syscall.SIGABRT)
	<-sigs
	logger.Info("terminating")
}
