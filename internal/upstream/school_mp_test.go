package upstream

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

func TestMPEventBase(t *testing.T) {
	a := &auth.Auth{UID: "u-1", Nickname: "测试"}
	base := mpEventBase(a)
	for _, k := range []string{"ideType", "extName", "ideName", "platform", "userId"} {
		if _, ok := base[k]; !ok {
			t.Errorf("missing common field %s", k)
		}
	}
	if base["ideType"] != "WorkBuddy_MP" || base["extName"] != "workbuddy-mp" {
		t.Errorf("fingerprint ideType=%v extName=%v", base["ideType"], base["extName"])
	}
}

func TestSchoolChatTimesEvents(t *testing.T) {
	ev := SchoolChatTimesEvents("conv-1")
	if ev["eventCode"] != "chat_request_send" {
		t.Errorf("eventCode=%v", ev["eventCode"])
	}
	if ev["conversationId"] != "conv-1" || ev["codebuddy.session_id"] != "conv-1" {
		t.Errorf("conversation join fields missing: %v", ev)
	}
	b, _ := json.Marshal(ev)
	if !strings.Contains(string(b), "agentName") {
		t.Error("agentName missing")
	}
}


// TestSchoolExpertUseEvents 锁定 school 域 expert_use 四事件链契约。
// 移植自 qiaoba223 fork。注意与 growth 域的 MiniExpertUseEvent 是两套口径，
// 本测试只覆盖 school 域（带 conversationId、type=16-BackToSchool、四事件）。
func TestSchoolExpertUseEvents(t *testing.T) {
	events := SchoolExpertUseEvents("ex_x", "论文写作导师", "conv-2")
	if len(events) != 4 {
		t.Fatalf("events=%d want 4", len(events))
	}
	wantCodes := []string{"expert_summon_click", "expert_summoned", "expert_actual_use", "chat_request_send"}
	for i, code := range wantCodes {
		if events[i]["eventCode"] != code {
			t.Errorf("events[%d].eventCode=%v want %s", i, events[i]["eventCode"], code)
		}
	}
	if events[0]["id"] != "ex_x" || events[0]["expertTitle"] != "论文写作导师" {
		t.Errorf("expert fields: %v", events[0])
	}
	if events[3]["expertId"] != "ex_x" {
		t.Errorf("chat expertId=%v", events[3]["expertId"])
	}
	// chat_request_send 必须带回 conversationId，否则服务端无法关联会话，
	// expert_use 判定不成立（school 域与 growth 域的关键差异）。
	if events[3]["conversationId"] != "conv-2" {
		t.Errorf("chat conversationId=%v want conv-2", events[3]["conversationId"])
	}
	if events[3]["parentConversationId"] != "conv-2" {
		t.Errorf("chat parentConversationId=%v want conv-2", events[3]["parentConversationId"])
	}
}
