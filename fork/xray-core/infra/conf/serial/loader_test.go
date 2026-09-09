package serial_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/xtls/xray-core/infra/conf/serial"
)

func TestLoaderError(t *testing.T) {
	testCases := []struct {
		Input  string
		Output string
	}{
		{
			Input: `{
				"log": {
					// abcd
					0,
					"loglevel": "info"
				}
		}`,
			Output: "offset 27",
		},
		{
			Input: `{
				"log": {
					// abcd
					"loglevel": "info",
				}
		}`,
			Output: "offset 51",
		},
		{
			Input: `{
				"port": 1,
				"inbounds": [{
					"protocol": "test"
				}]
		}`,
			Output: "parse json config",
		},
		{
			Input: `{
				"inbounds": [{
					"port": 1,
					"listen": 0,
					"protocol": "test"
				}]
		}`,
			Output: "offset 1",
		},
	}
	for _, testCase := range testCases {
		reader := bytes.NewReader([]byte(testCase.Input))
		_, err := serial.LoadJSONConfig(reader)
		errString := err.Error()
		if !strings.Contains(errString, testCase.Output) {
			t.Error("unexpected output from json: ", testCase.Input, ". expected ", testCase.Output, ", but actually ", errString)
		}
	}
}
