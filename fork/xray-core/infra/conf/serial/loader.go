package serial

import (
	"encoding/json"
	"io"

	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf"
	json_reader "github.com/xtls/xray-core/infra/conf/json"
)

// DecodeJSONConfig reads from reader and decode the config into *conf.Config
// syntax error could be detected.
func DecodeJSONConfig(reader io.Reader) (*conf.Config, error) {
	jsonConfig := &conf.Config{}

	// ponytail: raw json offset; bespoke line/char walk deleted.
	decoder := json.NewDecoder(&json_reader.Reader{
		Reader: reader,
	})

	if err := decoder.Decode(jsonConfig); err != nil {
		var offset int64
		switch tErr := errors.Cause(err).(type) {
		case *json.SyntaxError:
			offset = tErr.Offset
		case *json.UnmarshalTypeError:
			offset = tErr.Offset
		}
		if offset != 0 {
			return nil, errors.New("failed to read config file at offset ", offset).Base(err)
		}
		return nil, errors.New("failed to read config file").Base(err)
	}

	return jsonConfig, nil
}

func LoadJSONConfig(reader io.Reader) (*core.Config, error) {
	jsonConfig, err := DecodeJSONConfig(reader)
	if err != nil {
		return nil, err
	}

	pbConfig, err := jsonConfig.Build()
	if err != nil {
		return nil, errors.New("failed to parse json config").Base(err)
	}

	return pbConfig, nil
}
