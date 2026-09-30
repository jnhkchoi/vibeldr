package config

import (
	"encoding/json"
	"testing"
)

// JSON writes nothing when no channel could send, and otherwise the field
// names the helper's notifyConfig reads.
//
// JSON 은 보낼 채널이 없으면 아무것도 쓰지 않고, 있으면 헬퍼의 notifyConfig 가
// 읽는 이름으로 쓴다.
func TestNotifyJSON(t *testing.T) {
	if (Notify{Email: NotifyEmail{SMTP: "smtp.example:587", From: "a@example"}}).JSON() != nil {
		t.Fatal("a mail without a recipient was carried")
	}
	raw := Notify{WebhookURL: "https://hook.example/x", Email: NotifyEmail{SMTP: "smtp.example:587", From: "a@example", To: "b@example", Username: "u", Password: "p"}}.JSON()
	var got struct {
		WebhookURL string `json:"webhook_url"`
		Email      struct {
			SMTP, From, To, Username, Password string
		} `json:"email"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.WebhookURL != "https://hook.example/x" || got.Email.SMTP != "smtp.example:587" || got.Email.To != "b@example" || got.Email.Password != "p" {
		t.Fatalf("got %s", raw)
	}
}
