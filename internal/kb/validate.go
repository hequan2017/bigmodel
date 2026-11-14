package kb

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// MaxUploadBytes 是单个上传文件的大小上限（5 MiB）。
const MaxUploadBytes = 5 << 20

// allowedExts 是允许的扩展名与对应媒体类型。
// PDF/DOCX、OCR、音视频等均不在 P0 范围。
var allowedExts = map[string]string{
	".txt":      "text/plain",
	".md":       "text/markdown",
	".markdown": "text/markdown",
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

func invalidFile(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidFile, fmt.Sprintf(format, args...))
}

// sanitizeDisplayName 清理展示文件名：只取基础名，去除路径成分。
func sanitizeDisplayName(name string) string {
	name = strings.TrimSpace(name)
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "." || name == "/" {
		return ""
	}
	return name
}

// TrimBOM 去除 UTF-8 BOM。
func TrimBOM(content []byte) []byte {
	if len(content) >= 3 && string(content[:3]) == string(utf8BOM) {
		return content[3:]
	}
	return content
}

// normalizeAndValidate 校验并规范化上传文件。
// 返回清理后的展示名、媒体类型与去除 BOM 后的内容。
func normalizeAndValidate(displayName, contentType string, content []byte) (string, string, []byte, error) {
	cleanName := sanitizeDisplayName(displayName)
	if cleanName == "" {
		return "", "", nil, invalidFile("文件名为空")
	}
	if len([]rune(cleanName)) > 255 {
		return "", "", nil, invalidFile("文件名过长")
	}
	if len(content) > MaxUploadBytes {
		return "", "", nil, ErrFileTooLarge
	}

	ext := strings.ToLower(filepath.Ext(cleanName))
	mediaType, ok := allowedExts[ext]
	if !ok {
		return "", "", nil, invalidFile("仅支持 TXT 与 Markdown 文件")
	}

	base := strings.TrimSpace(strings.ToLower(strings.Split(contentType, ";")[0]))
	if base != "" && base != "application/octet-stream" && !strings.HasPrefix(base, "text/") {
		return "", "", nil, invalidFile("内容类型 %s 不支持", base)
	}

	content = TrimBOM(content)
	if len(content) == 0 {
		return "", "", nil, invalidFile("文件内容为空")
	}
	if !utf8.Valid(content) {
		return "", "", nil, invalidFile("文件必须是 UTF-8 编码")
	}
	return cleanName, mediaType, content, nil
}
