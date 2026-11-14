package kb

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestNormalizeAndValidateAcceptsMarkdown(t *testing.T) {
	name, mediaType, normalized, err := normalizeAndValidate("课件.md", "text/markdown", []byte("# 标题\n第一段。"))
	if err != nil {
		t.Fatalf("合法 Markdown 应通过: %v", err)
	}
	if name != "课件.md" || mediaType != "text/markdown" {
		t.Errorf("name=%s mediaType=%s", name, mediaType)
	}
	if !bytes.HasPrefix(normalized, []byte("# 标题")) {
		t.Errorf("内容被意外修改: %q", normalized)
	}
}

func TestNormalizeAndValidateStripsBOM(t *testing.T) {
	_, _, normalized, err := normalizeAndValidate("a.md", "", append([]byte{0xEF, 0xBB, 0xBF}, []byte("正文")...))
	if err != nil {
		t.Fatalf("带 BOM 应通过: %v", err)
	}
	if !bytes.HasPrefix(normalized, []byte("正文")) {
		t.Errorf("BOM 未去除: %q", normalized)
	}
}

func TestNormalizeAndValidateSanitizesPath(t *testing.T) {
	name, _, _, err := normalizeAndValidate("../../etc/passwd.md", "", []byte("x"))
	if err != nil {
		t.Fatalf("应通过: %v", err)
	}
	if name != "passwd.md" {
		t.Errorf("路径成分未清理: %q", name)
	}
	name, _, _, err = normalizeAndValidate("..\\win\\path.txt", "", []byte("x"))
	if err != nil {
		t.Fatalf("应通过: %v", err)
	}
	if name != "path.txt" {
		t.Errorf("Windows 路径成分未清理: %q", name)
	}
}

func TestNormalizeAndValidateRejects(t *testing.T) {
	cases := []struct {
		name        string
		displayName string
		contentType string
		content     []byte
		wantErr     error
	}{
		{"不支持扩展名", "图.png", "image/png", []byte("x"), ErrInvalidFile},
		{"不支持内容类型", "a.txt", "application/zip", []byte("x"), ErrInvalidFile},
		{"空文件", "a.txt", "text/plain", nil, ErrInvalidFile},
		{"非 UTF-8", "a.txt", "text/plain", []byte{0xFF, 0xFE, 'A'}, ErrInvalidFile},
		{"空文件名", "  ", "text/plain", []byte("x"), ErrInvalidFile},
		{"超大小上限", "a.txt", "text/plain", bytes.Repeat([]byte("a"), MaxUploadBytes+1), ErrFileTooLarge},
	}
	for _, tc := range cases {
		_, _, _, err := normalizeAndValidate(tc.displayName, tc.contentType, tc.content)
		if !errors.Is(err, tc.wantErr) {
			t.Errorf("%s: 期望 %v, got %v", tc.name, tc.wantErr, err)
		}
	}
}

func TestNormalizeAndValidateAcceptsOctetStream(t *testing.T) {
	_, mediaType, _, err := normalizeAndValidate("notes.txt", "application/octet-stream", []byte("hello"))
	if err != nil {
		t.Fatalf("octet-stream 应通过: %v", err)
	}
	if mediaType != "text/plain" {
		t.Errorf("mediaType = %s", mediaType)
	}
}

func TestValidateErrorMessageContainsContext(t *testing.T) {
	_, _, _, err := normalizeAndValidate("a.docx", "", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "TXT") {
		t.Errorf("错误信息应包含原因: %v", err)
	}
}
