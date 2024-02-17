package styx

import (
	"time"

	"github.com/alfred-landrum/fromenv"
)

type (
	config struct {
		DevPath   string `env:"styx_devpath=/dev/cachefiles"`
		CachePath string `env:"styx_cachepath=/var/cache/styx"`
	}
)

func loadConfig() *config {
	var c config
	if err := fromenv.Unmarshal(&c, fromenv.SetFunc(setDuration)); err != nil {
		panic(err)
	}
	return &c
}

func setDuration(t *time.Duration, s string) error {
	x, err := time.ParseDuration(s)
	*t = x
	return err
}
