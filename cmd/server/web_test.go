package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebEditorUsesPrivateAdminSessionAndUnifiedSecureCredential(t *testing.T) {
	rec := httptest.NewRecorder()
	(&app{}).webIndex(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("web index status = %d", rec.Code)
	}
	for _, required := range []string{
		"标签（逗号分隔，可留空清除）",
		"tags:tags.split(',').map(x=>x.trim()).filter(Boolean)",
		"Private 管理写入使用 HttpOnly 会话及 CSRF 防护",
		"MEMDOCK_ADMIN_PASSWORD",
		"X-MemDock-CSRF",
		"admin-change-password",
		"state.mode==='private'&&!state.admin",
		"Secure 模式：所有访问均需共享密钥；通过 HTTPS 直接读写。",
		"data-move=",
		"payload.target_project_id=target.id",
		"/api/v1/memories/'+encodeURIComponent(id)+'/move",
		"prompt('移动目标：\\n1. 项目记忆\\n2. 共享知识库\\n输入序号：')",
	} {
		if !strings.Contains(rec.Body.String(), required) {
			t.Errorf("web editor missing expected regression guard %q", required)
		}
	}
	if strings.Contains(rec.Body.String(), "window.crypto.subtle") || strings.Contains(rec.Body.String(), "输入 MEMDOCK_APPROVAL_KEY") {
		t.Error("Private Console still depends on browser HMAC signing")
	}
}
