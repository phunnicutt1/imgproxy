package imgix

import (
	"errors"
	"github.com/imgproxy/imgproxy/v4/ensure"
	"github.com/imgproxy/imgproxy/v4/env"
)

var (
	IMGPROXY_IMGIX_COMPAT        = env.Bool("IMGPROXY_IMGIX_COMPAT")
	IMGPROXY_IMGIX_SOURCE_PREFIX = env.String("IMGPROXY_IMGIX_SOURCE_PREFIX")
	IMGPROXY_IMGIX_SIGN_KEY      = env.String("IMGPROXY_IMGIX_SIGN_KEY")
)

type Config struct {
	Compat       bool
	SourcePrefix string
	SignKey      string
}

func NewDefaultConfig() Config { return Config{} }

func LoadConfigFromEnv(c *Config) (*Config, error) {
	c = ensure.Ensure(c, NewDefaultConfig)
	return c, errors.Join(
		IMGPROXY_IMGIX_COMPAT.Parse(&c.Compat),
		IMGPROXY_IMGIX_SOURCE_PREFIX.Parse(&c.SourcePrefix),
		IMGPROXY_IMGIX_SIGN_KEY.Parse(&c.SignKey),
	)
}

func (c *Config) Validate() error {
	if !c.Compat {
		return nil
	}
	if _, err := (Result{}).SourceURL(c.SourcePrefix); err != nil {
		return IMGPROXY_IMGIX_SOURCE_PREFIX.Errorf("must be an absolute source URL prefix without query or fragment")
	}
	return nil
}
