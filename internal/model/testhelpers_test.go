package model

import (
	"encoding/json"
	"io"
)

// jsonDecode 是测试辅助：从 reader 解码 JSON。
func jsonDecode(r io.Reader, v any) error {
	return json.NewDecoder(r).Decode(v)
}
