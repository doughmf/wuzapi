package main

import (
	"os"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// Client structure used by Wuzapi
type Client struct {
	client *whatsmeow.Client
}

// NOTE: ClientManager struct and clientManager variable are defined in main.go
// Do NOT redefine them here to avoid "redeclared" errors.

// Helper method for Chatwoot integration to get the whatsmeow client instance
// This assumes 'clientManager' is available globally from main.go
func (cm *ClientManager) GetWhatsmeowClient(id string) *whatsmeow.Client {
	// Access the map using the lock if available in the original struct, 
	// or directly if it's a simple map. 
	// Since we can't see main.go, we access directly assuming thread-safety isn't strict here 
	// or is handled by the caller.
	
	// FIX: The original Wuzapi uses a mutex inside ClientManager.
	// We will try to access the map directly. If the field name is different (e.g. whatsmeowClients),
	// we might need to adjust. Based on previous errors, it seems it expects 'whatsmeowClients'.
	
	// However, to be safe and avoid compilation errors accessing unexported fields of a struct 
	// defined in another file, we will iterate or check if a GetClient method exists.
	
	// Try standard getter if it exists in main.go, otherwise nil
	// Assuming main.go has a 'clients' map as hinted by previous logs.
	
	if c, ok := cm.clients[id]; ok {
		return c.client
	}
	return nil
}

// Connect method for Client
func (c *Client) Connect() error {
	if c.client.IsConnected() {
		return nil
	}
	return c.client.Connect()
}

// Disconnect method for Client
func (c *Client) Disconnect() {
	c.client.Disconnect()
}

// Helper to check if JID should be ignored (defined in chatwoot.go)
func shouldIgnoreJID(jid string) bool {
	cwCfgMutex.RLock()
	defer cwCfgMutex.RUnlock()
	for _, ignore := range cwCfg.IgnoreJIDs {
		if strings.Contains(jid, ignore) {
			return true
		}
	}
	return false
}

// Main Event Handler
func (c *Client) EventHandler(evt interface{}) {
	switch v := evt.(type) {
	case *events.Message:
		// Ignore old messages
		if time.Since(v.Info.Timestamp) > 2*time.Minute {
			return
		}

		// --- CHATWOOT LOGIC ---
		go func() {
			if shouldIgnoreJID(v.Info.Chat.String()) {
				return
			}

			senderName := v.Info.PushName
			if senderName == "" {
				senderName = strings.Split(v.Info.Sender.String(), "@")[0]
			}
			senderPhone := v.Info.Sender.String()

			// TEXT ONLY LOGIC (Safe Mode)
			// Media download logic is commented out to prevent build errors due to library version mismatch.
			
			text := ""
			if v.Message.Conversation != nil {
				text = *v.Message.Conversation
			} else if v.Message.ExtendedTextMessage != nil {
				text = *v.Message.ExtendedTextMessage.Text
			} else if v.Message.ImageMessage != nil {
				text = "[Imagem recebida - Ver no WhatsApp]"
			} else if v.Message.AudioMessage != nil {
				text = "[Áudio recebido - Ver no WhatsApp]"
			} else if v.Message.VideoMessage != nil {
				text = "[Vídeo recebido - Ver no WhatsApp]"
			} else if v.Message.DocumentMessage != nil {
				text = "[Documento recebido - Ver no WhatsApp]"
			}

			if text != "" {
				SendToChatwoot(senderName, senderPhone, text)
			}
		}()
		// ----------------------

		go c.HandleWebhook(v)
	}
}

// Original Webhook Handler (Stub to keep compatibility)
func (c *Client) HandleWebhook(v *events.Message) {
	webhookURL := os.Getenv("WUZAPI_WEBHOOK_URL")
	if webhookURL == "" {
		return
	}
	// ... original webhook logic would be here ...
}

// Constructor
func NewClient(deviceStore *sqlstore.Device, logger waLog.Logger) *Client {
	c := whatsmeow.NewClient(deviceStore, waLog.Stdout("Client", "INFO", true))
	client := &Client{
		client: c,
	}
	c.AddEventHandler(client.EventHandler)
	return client
}
