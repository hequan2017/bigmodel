package httpserver

import "strconv"

// int64ToString 是审计资源 ID 的格式化辅助。
func int64ToString(v int64) string {
	return strconv.FormatInt(v, 10)
}
