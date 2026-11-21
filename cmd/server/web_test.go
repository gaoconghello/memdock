package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebEditorKeepsEditableTagsAndBlocksInsecureSigning(t *testing.T) {
	rec := httptest.NewRecorder()
	(&app{}).webIndex(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("web index status = %d", rec.Code)
	}
	for _, required := range []string{
		"标签（逗号分隔，可留空清除）",
		"tags:tags.split(',').map(x=>x.trim()).filter(Boolean)",
		"!window.isSecureContext||!window.crypto?.subtle",
		"已阻止密钥输入和写入",
		"window.crypto.subtle.importKey",
		"state.mode==='secure'",
		"生成此操作的审批请求文件",
		"approve-request memdock-approval-request.json",
		"Secure 模式：写操作仅导出审批请求文件",
		"审批请求已下载，数据尚未修改",
	} {
		if !strings.Contains(rec.Body.String(), required) {
			t.Errorf("web editor missing expected regression guard %q", required)
		}
	}
}
