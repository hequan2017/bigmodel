package httpserver

import (
	"encoding/json"
	"io"
)

// jsonDecode 从 JSON 请求体解码到目标结构。
func jsonDecode(r io.Reader, v any) error {
	return json.NewDecoder(r).Decode(v)
}
