package msgxwhatsapp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Conversia-AI/craftable-conversia/msgx"
)

func TestIsBSUID(t *testing.T) {
	valid := []string{"PE.3586160921531832", "US.13491208655302741918", "US.ENT.11815799212886844830", " PE.3586160921531832 "}
	for _, input := range valid {
		if !isBSUID(input) {
			t.Fatalf("isBSUID(%q) = false, want true", input)
		}
	}
	invalid := []string{"", "51999935341", "+51999935341", "pe.3586160921531832", "PE3586160921531832", "PE."}
	for _, input := range invalid {
		if isBSUID(input) {
			t.Fatalf("isBSUID(%q) = true, want false", input)
		}
	}
}

func TestCleanPhoneNumberPassesBSUIDThrough(t *testing.T) {
	w := &WhatsAppProvider{}
	if got := w.cleanPhoneNumber("PE.3586160921531832"); got != "PE.3586160921531832" {
		t.Fatalf("cleanPhoneNumber(BSUID) = %q, want passthrough", got)
	}
	if got := w.cleanPhoneNumber("51999935341"); got != "+51999935341" {
		t.Fatalf("phone cleaning changed: %q", got)
	}
}

// A BSUID destination must go in the "recipient" field, never digit-cleaned into "to"
// (that produced Meta error 131026 Message undeliverable).
func TestConvertToWhatsAppMessageBSUIDUsesRecipientField(t *testing.T) {
	w := &WhatsAppProvider{}
	msg, err := w.convertToWhatsAppMessage(context.Background(), msgx.Message{
		To:   "PE.3586160921531832",
		Type: msgx.MessageTypeText,
		Content: msgx.Content{
			Text: &msgx.TextContent{Body: "hola"},
		},
	})
	if err != nil {
		t.Fatalf("convertToWhatsAppMessage: %v", err)
	}

	if msg.Recipient != "PE.3586160921531832" {
		t.Fatalf("Recipient = %q, want the BSUID intact", msg.Recipient)
	}
	if msg.To != "" {
		t.Fatalf("To = %q, want empty for BSUID destination", msg.To)
	}

	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), "\"to\"") {
		t.Fatalf("payload must omit \"to\" for BSUID destinations: %s", body)
	}
	if !strings.Contains(string(body), "\"recipient\":\"PE.3586160921531832\"") {
		t.Fatalf("payload must carry recipient: %s", body)
	}
}

func TestConvertToWhatsAppMessagePhoneKeepsToField(t *testing.T) {
	w := &WhatsAppProvider{}
	msg, err := w.convertToWhatsAppMessage(context.Background(), msgx.Message{
		To:   "51999935341",
		Type: msgx.MessageTypeText,
		Content: msgx.Content{
			Text: &msgx.TextContent{Body: "hola"},
		},
	})
	if err != nil {
		t.Fatalf("convertToWhatsAppMessage: %v", err)
	}
	if msg.To != "+51999935341" || msg.Recipient != "" {
		t.Fatalf("phone destination changed: to=%q recipient=%q", msg.To, msg.Recipient)
	}
}
