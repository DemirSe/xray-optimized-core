package serial

import (
	"context"
	"encoding/json"

	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/main/confloader"
)

func MergeConfigFromFiles(files []*core.ConfigSource) (string, error) {
	c, err := mergeConfigs(files)
	if err != nil {
		return "", err
	}

	// ponytail: debug dump only; stdlib replaces 230-line reflect dumper.
	b, err := json.MarshalIndent(c, "", "    ")
	if err != nil {
		return "", errors.New("marshal to json failed.").Base(err).AtError()
	}
	return string(b), nil
}

func mergeConfigs(files []*core.ConfigSource) (*conf.Config, error) {
	cf := &conf.Config{}
	for i, file := range files {
		errors.LogInfo(context.Background(), "Reading config: ", file)
		r, err := confloader.LoadConfig(file.Name)
		if err != nil {
			return nil, errors.New("failed to read config: ", file).Base(err)
		}
		c, err := DecodeJSONConfig(r)
		if err != nil {
			return nil, errors.New("failed to decode config: ", file).Base(err)
		}
		if i == 0 {
			*cf = *c
			continue
		}
		cf.Override(c, file.Name)
	}
	return cf, nil
}

func BuildConfig(files []*core.ConfigSource) (*core.Config, error) {
	config, err := mergeConfigs(files)
	if err != nil {
		return nil, err
	}
	return config.Build()
}

func init() {
	core.ConfigBuilderForFiles = BuildConfig
	core.ConfigMergedFormFiles = MergeConfigFromFiles
}
